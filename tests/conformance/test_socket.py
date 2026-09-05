"""The socket transport: the upgrade, the envelope, and what is refused.

Nothing here is about chat. It is about the frame format, and about the rule
that a bad frame costs the sender a frame rather than a connection.
"""

import json
import time

import pytest

from .wire import APPLICATION_MESSAGE, Socket

# Policy violation. The upgrade completes and the server then closes.
CLOSE_POLICY_VIOLATION = 1008

# Long enough to prove a frame did not arrive, short enough to keep the suite
# quick. Every send here is local and answered in milliseconds when answered.
SILENCE = 1.0


def test_an_anonymous_upgrade_is_closed(server):
    socket = Socket(server.base).connect()
    try:
        assert socket.closed.wait(timeout=5)
        assert socket.close_reason == (CLOSE_POLICY_VIOLATION, "Not authenticated")
    finally:
        socket.close()


def test_the_handshake_is_the_first_frame(server, session):
    http = session("demo")
    socket = Socket(server.base, http.cookie_header()).connect()
    try:
        frame = socket.handshake()
        assert frame["name"] == "osjs/core:connected"
        assert frame["params"][0]["cookie"]["maxAge"] > 0
    finally:
        socket.close()


def test_a_reply_quotes_the_pid_it_was_sent_with(demo):
    """Several requests in flight are told apart by nothing else."""
    first = demo.send_op("sync")
    second = demo.send_op("sync")
    assert demo.await_reply(second) is not None
    assert demo.await_reply(first) is not None


def test_a_push_carries_a_null_pid_and_the_application_name(demo, alice, unique):
    room = demo.call("create", title=unique("Push"), invite=["alice"])
    alice.drain()
    demo.call("send", room=room["id"], body="hello")

    # The reader sorts by pid, so anything reaching `pushes` had a null one.
    event = alice.expect_push(lambda e: e.get("type") == "message")
    assert event["body"] == "hello"


@pytest.mark.parametrize(
    "frame",
    [
        "not json at all",
        json.dumps({"params": []}),
        json.dumps({"name": 7, "params": []}),
        json.dumps({"name": APPLICATION_MESSAGE, "params": "not a list"}),
        json.dumps({"name": APPLICATION_MESSAGE, "params": []}),
        json.dumps({"name": APPLICATION_MESSAGE, "params": ["not an object"]}),
        json.dumps([1, 2, 3]),
    ],
)
def test_a_malformed_frame_is_dropped_without_closing_the_socket(demo, frame):
    demo.send_frame(frame)
    assert demo.call("sync")["me"] == "demo"


def test_an_internal_message_name_does_nothing(demo):
    """A page must not be able to fabricate an event the server trusts.

    What is observable is that the frame does nothing. Whether the server
    refused it as forged or dropped it as unhandled cannot be told apart from
    out here, and a reimplementation is free to do either -- but it may not
    grow a handler for one of these names.
    """
    demo.send_frame({"name": "osjs/core:logged-in", "params": [{"username": "root"}]})
    time.sleep(SILENCE)
    assert demo.control.empty()
    assert demo.call("sync")["me"] == "demo"


def test_an_unhandled_name_is_dropped(demo):
    demo.send_frame({"name": "something/else", "params": []})
    assert demo.call("sync")["me"] == "demo"


def test_an_application_with_no_handler_answers_nothing(demo):
    pid = 4242
    demo.send_frame(
        {
            "name": APPLICATION_MESSAGE,
            "params": [{"pid": pid, "name": "NoSuchApp", "args": [{}]}],
        }
    )
    time.sleep(SILENCE)
    assert demo.pushes.empty()
    assert demo.call("sync")["me"] == "demo"


def test_a_handler_that_fails_answers_an_error_rather_than_closing(demo):
    """One bad field must not cost a client its connection."""
    assert demo.refuse("open", invite=7) == "Request failed"
    assert demo.call("sync")["me"] == "demo"


def test_the_keepalive_arrives_after_a_silence(fresh_server, session):
    from .wire import Http

    server = fresh_server(MINOS_WS_PING=1)
    http = Http(server.base)
    http.login("demo", "demo")

    socket = Socket(server.base, http.cookie_header()).connect()
    try:
        socket.handshake()
        frame = socket.expect_control(lambda f: f["name"] == "osjs/core:ping", timeout=5)
        assert frame["params"] == []
    finally:
        socket.close()
