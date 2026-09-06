"""The ZeroMQ message bus.

`Registry` fans out to the websockets of one process. That is the whole story
only while there is one process: the registry is a set in memory, so under
gunicorn with several workers a broadcast would reach the users who happened to
land on the same worker and silently miss the rest. This module is the leg
between processes. A worker publishes what its clients produce, subscribes to
the topics its clients are watching, and hands what arrives back to the
registry for local delivery.

Topology is the standard forwarder: producers PUB into an XSUB, subscribers SUB
from an XPUB, and a proxy joins them so neither side needs to know the other
exists. Whichever process starts first owns the proxy.

Two properties of PUB/SUB shape everything above this file:

- A subscription has to travel to every publisher before it matches anything,
  and what is published in the meantime is dropped -- the slow joiner. So no
  subscription travels: the proxy subscribes to everything once, each process
  takes everything from it, and which topics a process wants is a dict it
  tests locally. A subscription therefore holds the instant it is asked for,
  which matters because opening a room and speaking in it are one round trip
  apart. What this costs is every process reading every message.

- Delivery is still fire-and-forget: anything past the high water mark is
  dropped, and a PUB that has just connected sends nothing until its link is
  up. Nothing here retries or acknowledges. Recovery is the sequence number in
  `timeline`: a client that sees a gap asks for the difference.
"""

import atexit
import logging
import os
import threading
import time
import uuid
from pathlib import Path

import zmq

from .timeline import decode, encode

logger = logging.getLogger(__name__)

# How long to let a fresh PUB connection settle before the first publish. A
# subscription that has not propagated drops what it should have matched, and
# while the sequence numbers repair that, not provoking it is cheaper.
SETTLE = 0.15

PRESENCE_TOPIC = b"presence|"


def room_topic(room_id):
    """The topic a room's messages are published on.

    Terminated, so that the name cannot be a prefix of another room's: `room.1`
    would otherwise be one of `room.11`, which matters to anything matching
    these by prefix rather than whole.
    """
    return f"room.{room_id}|".encode()


def _ipc_path(endpoint):
    return endpoint[len("ipc://") :] if endpoint.startswith("ipc://") else None


class Broker:
    """The XSUB/XPUB proxy, and the claim on the right to run it.

    Ownership is a POSIX file lock rather than a successful bind. A bind that
    fails cannot distinguish a live peer from an `ipc://` socket file left
    behind by a process that was killed, whereas the kernel drops a lock when
    its holder dies. Holding the lock also makes it safe to clear those stale
    files before binding.
    """

    def __init__(self, xsub, xpub, run_dir):
        self.xsub = xsub
        self.xpub = xpub
        self.run_dir = Path(run_dir)
        self._lock_file = None
        self._context = None

    def claim(self):
        self.run_dir.mkdir(parents=True, exist_ok=True)
        try:
            import fcntl
        except ImportError:  # pragma: no cover - POSIX only, and this is Linux
            return True

        self._lock_file = open(self.run_dir / "broker.lock", "w")
        try:
            fcntl.flock(self._lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            self._lock_file.close()
            self._lock_file = None
            return False
        return True

    def start(self):
        """Run the proxy in a daemon thread. True when this process owns it."""
        if not self.claim():
            logger.info("Message bus proxy is owned by another process")
            return False

        for endpoint in (self.xsub, self.xpub):
            path = _ipc_path(endpoint)
            if path is not None and os.path.exists(path):
                # Ours to remove: nothing else holds the lock.
                os.unlink(path)

        self._context = zmq.Context()
        frontend = self._context.socket(zmq.XSUB)
        backend = self._context.socket(zmq.XPUB)
        try:
            frontend.bind(self.xsub)
            backend.bind(self.xpub)
        except Exception:
            # A bind fails for reasons the caller cannot always foresee: an
            # `ipc://` path over the 103-byte `sockaddr_un` limit, a run
            # directory that has been removed. Left open, the sockets keep the
            # context from terminating and the process hangs at interpreter
            # exit instead of reporting what went wrong.
            frontend.close(0)
            backend.close(0)
            self.stop()
            raise

        # Subscribe the proxy to everything, so no publisher ever filters. A
        # PUB sends only what a subscription it has already received matches,
        # and subscriptions reach it from here -- so a subscriber joining after
        # a publisher started would otherwise race every publisher's copy of
        # its subscription, and lose whatever was sent in between. Which topics
        # a process wants is decided in `Bus._relay` instead.
        frontend.send(b"\x01")

        def run():
            try:
                zmq.proxy(frontend, backend)
            except zmq.ContextTerminated:
                pass
            finally:
                frontend.close(0)
                backend.close(0)

        threading.Thread(target=run, name="bus-proxy", daemon=True).start()
        atexit.register(self.stop)
        logger.info("Message bus proxy on %s -> %s", self.xsub, self.xpub)
        return True

    def stop(self):
        if self._context is not None:
            self._context.term()
            self._context = None
        if self._lock_file is not None:
            self._lock_file.close()
            self._lock_file = None


class Bus:
    """One process's connection to the bus.

    ZeroMQ contexts are thread-safe and sockets are not, which matters here
    because flask-sock runs a thread per websocket and every one of them wants
    to publish. So no socket is ever touched by two threads: publishers hand
    frames to a sender thread over an `inproc` queue, subscription changes go
    to the relay thread the same way, and the sender and relay each own their
    sockets outright.
    """

    def __init__(self, xsub, xpub):
        self.xsub = xsub
        self.xpub = xpub

        self._context = zmq.Context.instance()
        self._id = uuid.uuid4().hex[:8]
        self._outbound = f"inproc://minos-out-{self._id}"
        self._control = f"inproc://minos-control-{self._id}"
        self._local = threading.local()
        self._threads = []
        self._on_message = None
        self._started = threading.Event()

        # What this process wants delivered, counted per topic. Plain state
        # under a lock rather than a socket option, because a subscription has
        # to hold the moment it is asked for: the caller's next act is often to
        # publish to the topic it just subscribed to.
        self._topics = {}
        self._topics_lock = threading.Lock()

    # -- lifecycle ------------------------------------------------------------

    def start(self, on_message):
        """Begin publishing and relaying. `on_message(topic, payload)`."""
        self._on_message = on_message

        # Bound before any thread can connect: an inproc connect to an endpoint
        # nothing has bound yet fails outright.
        sender_in = self._context.socket(zmq.PULL)
        sender_in.bind(self._outbound)
        relay_in = self._context.socket(zmq.PULL)
        relay_in.bind(self._control)

        self._spawn("bus-sender", self._sender, sender_in)
        self._spawn("bus-relay", self._relay, relay_in)
        self._started.wait(timeout=2)

    def _spawn(self, name, target, socket):
        thread = threading.Thread(target=target, args=(socket,), name=name, daemon=True)
        thread.start()
        self._threads.append(thread)

    def stop(self):
        for endpoint in (self._outbound, self._control):
            try:
                self._push(endpoint).send_multipart([b"stop", b""])
            except zmq.ZMQError:  # pragma: no cover - shutting down anyway
                pass
        for thread in self._threads:
            thread.join(timeout=2)
        self._threads = []

    # -- the public API, callable from any thread -----------------------------

    def publish(self, topic, payload):
        self._push(self._outbound).send_multipart([b"send", topic, encode(payload)])

    def subscribe(self, topic):
        """Deliver `topic` to this process, in force by the time this returns.

        Counted rather than a set: two windows on the same room in one browser
        must not have the first one closed cancel the other's delivery.
        """
        with self._topics_lock:
            self._topics[topic] = self._topics.get(topic, 0) + 1

    def unsubscribe(self, topic):
        with self._topics_lock:
            if self._topics.get(topic):
                self._topics[topic] -= 1
                if not self._topics[topic]:
                    del self._topics[topic]

    def _push(self, endpoint):
        """A PUSH socket private to the calling thread.

        Thread-local rather than shared, because a socket handed to two threads
        interleaves the parts of a multipart frame.
        """
        sockets = getattr(self._local, "sockets", None)
        if sockets is None:
            sockets = self._local.sockets = {}
        socket = sockets.get(endpoint)
        if socket is None:
            socket = self._context.socket(zmq.PUSH)
            socket.connect(endpoint)
            sockets[endpoint] = socket
        return socket

    def release_thread(self):
        """Close the sockets this thread opened.

        `threading.local` drops its values when the thread ends, but a ZeroMQ
        socket is not reclaimed by being garbage collected -- it holds a file
        descriptor until it is closed. flask-sock runs a thread per websocket
        and every one of them publishes, so without this each connection would
        cost the process an fd for good. Called from the socket route's teardown,
        which runs in the connection's own thread.
        """
        sockets = getattr(self._local, "sockets", None)
        if not sockets:
            return
        for socket in sockets.values():
            socket.close(0)
        sockets.clear()

    # -- the two threads that own sockets -------------------------------------

    def _sender(self, inbox):
        """Drain the outbound queue onto the one PUB socket in this process."""
        publisher = self._context.socket(zmq.PUB)
        publisher.connect(self.xsub)

        # Nothing is lost by pausing here: callers push onto the inproc queue,
        # which buffers. Draining it only once the connection has settled is
        # what keeps the first message of a session from being a slow joiner.
        time.sleep(SETTLE)
        try:
            while True:
                frame = inbox.recv_multipart()
                if frame[0] == b"stop":
                    return
                publisher.send_multipart(frame[1:])
        except zmq.ZMQError:  # pragma: no cover - context torn down
            pass
        finally:
            publisher.close(0)
            inbox.close(0)

    def _relay(self, control):
        """Own the SUB socket, and drop what this process did not ask for.

        The socket takes everything and the filter is `Bus._topics`, so that
        subscribing is a local act with nothing to propagate. A SUB filter is
        a byte prefix and would have to travel to the XPUB before it matched;
        an exact test against a dict costs a comparison and holds at once.
        """
        subscriber = self._context.socket(zmq.SUB)
        subscriber.connect(self.xpub)
        subscriber.setsockopt(zmq.SUBSCRIBE, b"")

        poller = zmq.Poller()
        poller.register(subscriber, zmq.POLLIN)
        poller.register(control, zmq.POLLIN)

        self._started.set()
        try:
            while True:
                ready = dict(poller.poll())

                if control in ready:
                    action, _ = control.recv_multipart()
                    if action == b"stop":
                        return

                if subscriber in ready:
                    # The unpack is inside the guard with the decode: a frame of
                    # the wrong shape would otherwise raise ValueError here and
                    # take the relay thread down, silently ending delivery for
                    # every client on this worker. The bus is unauthenticated,
                    # so the frame shape is not ours to assume.
                    try:
                        topic, raw = subscriber.recv_multipart()
                        payload = decode(raw)
                    except ValueError:
                        logger.warning("Discarding malformed bus frame", exc_info=True)
                        continue
                    with self._topics_lock:
                        wanted = topic in self._topics
                    if not wanted:
                        continue
                    if self._on_message is not None:
                        self._on_message(topic, payload)
        except zmq.ZMQError:  # pragma: no cover - context torn down
            pass
        finally:
            subscriber.close(0)
            control.close(0)


def run_broker(xsub, xpub, run_dir):
    """Run a standalone proxy until killed.

    Hosts wire their own entry point around this; the module takes its
    endpoints rather than reading them from anywhere global.
    """
    logging.basicConfig(level=logging.INFO)
    broker = Broker(xsub, xpub, run_dir)
    if not broker.start():
        raise SystemExit("Another process already owns the message bus")
    threading.Event().wait()
