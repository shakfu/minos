"""The ZeroMQ leg, exercised against a real proxy rather than a fake.

These are the properties the rest of the design assumes: that a topic filter is
exact, that any thread may publish, and that a subscription outlives one of two
subscribers going away.
"""

import threading
import time

import pytest

# Long enough for a SUB filter to reach the proxy. There is no acknowledgement
# to wait on -- that is the nature of PUB/SUB -- so the tests give it a window.
PROPAGATION = 0.4


@pytest.fixture
def bus(app):
    """The application's own bus, already started and proxied."""
    return app.extensions["chat"].bus


def collector():
    received = []
    lock = threading.Lock()

    def on_message(topic, payload):
        with lock:
            received.append((topic, payload))

    return received, on_message


def wait_for(predicate, timeout=3):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return False


@pytest.fixture
def listening(bus):
    """Re-point the running bus at a collector of our own."""
    received, on_message = collector()
    bus._on_message = on_message
    return received


def test_a_published_message_comes_back_on_its_topic(bus, listening):
    from messaging import bus as module

    topic = module.room_topic("abc")
    bus.subscribe(topic)
    time.sleep(PROPAGATION)

    bus.publish(topic, {"hello": "world"})

    assert wait_for(lambda: len(listening) == 1)
    assert listening[0] == (topic, {"hello": "world"})


def test_a_topic_filter_is_exact_not_a_prefix(bus, listening):
    """`room.1` must not match `room.11`.

    SUB filtering is a byte-prefix comparison, so this is only true because the
    topics are terminated. Without the terminator the wrong room's traffic
    arrives silently, and only once someone has eleven rooms.
    """
    from messaging import bus as module

    bus.subscribe(module.room_topic("1"))
    time.sleep(PROPAGATION)

    bus.publish(module.room_topic("11"), {"room": "eleven"})
    bus.publish(module.room_topic("1"), {"room": "one"})

    assert wait_for(lambda: len(listening) == 1)
    time.sleep(PROPAGATION)
    assert [payload for _, payload in listening] == [{"room": "one"}]


def test_every_thread_may_publish(bus, listening):
    """Sockets are not thread-safe; the inproc funnel is what makes this safe.

    A shared PUB socket would interleave the parts of a multipart frame, so a
    failure here shows up as a torn or missing message rather than an error.
    """
    from messaging import bus as module

    topic = module.room_topic("threads")
    bus.subscribe(topic)
    time.sleep(PROPAGATION)

    threads = [
        threading.Thread(target=bus.publish, args=(topic, {"n": index})) for index in range(16)
    ]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    assert wait_for(lambda: len(listening) == 16)
    assert sorted(payload["n"] for _, payload in listening) == list(range(16))


def test_unsubscribing_one_of_two_watchers_keeps_delivery(bus, listening):
    """Two windows on one room: closing the first must not silence the second."""
    from messaging import bus as module

    topic = module.room_topic("counted")
    bus.subscribe(topic)
    bus.subscribe(topic)
    bus.unsubscribe(topic)
    time.sleep(PROPAGATION)

    bus.publish(topic, {"still": "here"})
    assert wait_for(lambda: len(listening) == 1)

    bus.unsubscribe(topic)
    time.sleep(PROPAGATION)
    bus.publish(topic, {"gone": True})
    time.sleep(PROPAGATION)
    assert len(listening) == 1


def test_a_second_broker_defers_to_the_one_holding_the_lock(app):
    """Ownership is a file lock, so a stale ipc file cannot lock everyone out."""
    from messaging import bus as module

    from server import config

    second = module.Broker(config.BUS_XSUB, config.BUS_XPUB, config.RUN_DIR)
    assert second.start() is False


def test_a_frame_of_the_wrong_shape_does_not_kill_the_relay(bus, listening):
    """The relay is the only delivery path a worker has.

    An unpack that raises would end its thread and silence every client on this
    worker, and the bus is unauthenticated -- so the shape is not ours to
    assume.
    """
    from messaging import bus as module

    topic = module.room_topic("shapes")
    bus.subscribe(topic)
    time.sleep(PROPAGATION)

    # One part where two are expected, then three: neither is a valid frame.
    bus._push(bus._outbound).send_multipart([b"send", topic])
    bus._push(bus._outbound).send_multipart([b"send", topic, b"{}", b"extra"])
    time.sleep(PROPAGATION)

    # The relay survived both and still delivers.
    bus.publish(topic, {"still": "alive"})
    assert wait_for(lambda: any(p == {"still": "alive"} for _, p in listening))


def test_a_frame_with_undecodable_json_does_not_kill_the_relay(bus, listening):
    from messaging import bus as module

    topic = module.room_topic("garbage")
    bus.subscribe(topic)
    time.sleep(PROPAGATION)

    bus._push(bus._outbound).send_multipart([b"send", topic, b"not json"])
    time.sleep(PROPAGATION)

    bus.publish(topic, {"still": "alive"})
    assert wait_for(lambda: any(p == {"still": "alive"} for _, p in listening))


def test_a_thread_releases_its_publisher_sockets(bus):
    """Each websocket thread opens PUSH sockets; nothing else closes them.

    A ZeroMQ socket is not reclaimed by garbage collection -- it holds a file
    descriptor until closed -- so without this each connection cost the process
    an fd for good.
    """
    opened = {}

    def publish_then_release():
        bus.publish(b"probe|", {"x": 1})
        opened["during"] = len(bus._local.sockets)
        bus.release_thread()
        opened["after"] = len(bus._local.sockets)

    thread = threading.Thread(target=publish_then_release)
    thread.start()
    thread.join()

    assert opened["during"] >= 1
    assert opened["after"] == 0
