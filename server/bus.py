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

- Filtering is a byte-prefix match, not a path match, so `room.1` would also
  match `room.11`. Topics are terminated with `|` to make the prefix exact.

- Delivery is fire-and-forget. Messages published before a subscription has
  propagated are dropped -- the slow joiner -- and so is anything past the high
  water mark. Nothing here retries or acknowledges. Recovery is the sequence
  number in `timeline`: a client that sees a gap asks for the difference.
"""

import atexit
import logging
import os
import threading
import time
import uuid

import zmq

from . import config, timeline

logger = logging.getLogger(__name__)

# How long to let a fresh PUB connection settle before the first publish. A
# subscription that has not propagated drops what it should have matched, and
# while the sequence numbers repair that, not provoking it is cheaper.
SETTLE = 0.15

PRESENCE_TOPIC = b"presence|"


def room_topic(room_id):
    """The topic a room's messages are published on.

    Terminated, because a SUB filter is a prefix and an unterminated `room.1`
    would swallow `room.11`.
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

    def __init__(self, xsub=None, xpub=None):
        self.xsub = xsub or config.BUS_XSUB
        self.xpub = xpub or config.BUS_XPUB
        self._lock_file = None
        self._context = None

    def claim(self):
        config.RUN_DIR.mkdir(parents=True, exist_ok=True)
        try:
            import fcntl
        except ImportError:  # pragma: no cover - POSIX only, and this is Linux
            return True

        self._lock_file = open(config.RUN_DIR / "broker.lock", "w")
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
        frontend.bind(self.xsub)
        backend.bind(self.xpub)

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

    def __init__(self, xsub=None, xpub=None):
        self.xsub = xsub or config.BUS_XSUB
        self.xpub = xpub or config.BUS_XPUB

        self._context = zmq.Context.instance()
        self._id = uuid.uuid4().hex[:8]
        self._outbound = f"inproc://minos-out-{self._id}"
        self._control = f"inproc://minos-control-{self._id}"
        self._local = threading.local()
        self._threads = []
        self._on_message = None
        self._started = threading.Event()

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
        self._push(self._outbound).send_multipart([b"send", topic, timeline.encode(payload)])

    def subscribe(self, topic):
        self._push(self._control).send_multipart([b"sub", topic])

    def unsubscribe(self, topic):
        self._push(self._control).send_multipart([b"unsub", topic])

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
        """Own the SUB socket: its subscriptions and everything it receives.

        Subscriptions are counted rather than set, because two windows on the
        same room in one browser must not have the first one closed cancel the
        other's delivery.
        """
        subscriber = self._context.socket(zmq.SUB)
        subscriber.connect(self.xpub)

        poller = zmq.Poller()
        poller.register(subscriber, zmq.POLLIN)
        poller.register(control, zmq.POLLIN)

        counts = {}
        self._started.set()
        try:
            while True:
                ready = dict(poller.poll())

                if control in ready:
                    action, topic = control.recv_multipart()
                    if action == b"stop":
                        return
                    if action == b"sub":
                        counts[topic] = counts.get(topic, 0) + 1
                        if counts[topic] == 1:
                            subscriber.setsockopt(zmq.SUBSCRIBE, topic)
                    elif action == b"unsub" and counts.get(topic):
                        counts[topic] -= 1
                        if counts[topic] == 0:
                            subscriber.setsockopt(zmq.UNSUBSCRIBE, topic)
                            del counts[topic]

                if subscriber in ready:
                    topic, raw = subscriber.recv_multipart()
                    try:
                        payload = timeline.decode(raw)
                    except ValueError:
                        logger.warning("Discarding malformed bus frame on %s", topic)
                        continue
                    if self._on_message is not None:
                        self._on_message(topic, payload)
        except zmq.ZMQError:  # pragma: no cover - context torn down
            pass
        finally:
            subscriber.close(0)
            control.close(0)


def run_broker():
    """Entry point for a standalone proxy: `python -m server.bus`."""
    logging.basicConfig(level=logging.INFO)
    broker = Broker()
    if not broker.start():
        raise SystemExit("Another process already owns the message bus")
    threading.Event().wait()


if __name__ == "__main__":
    run_broker()
