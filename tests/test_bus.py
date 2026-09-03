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
    from server import bus as module

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
    from server import bus as module

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
    from server import bus as module

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
    from server import bus as module

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
    from server import bus as module

    assert module.Broker().start() is False
