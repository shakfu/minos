"""The Chat handler, driven the way the client drives it.

Delivery here is the real path: a handler publishes onto ZeroMQ, the relay
thread receives it, and the registry writes it to the other user's websocket.
Nothing is stubbed between the two ends.
"""

import json
import time

import pytest

# Time for a SUB filter to reach the proxy. `sync` subscribes; a send before
# that has propagated is a slow joiner and is dropped by design.
PROPAGATION = 0.4


class FakeWebsocket:
    def __init__(self):
        self.sent = []

    def send(self, frame):
        self.sent.append(json.loads(frame))

    def pushes(self):
        """Chat events pushed to this socket, unwrapped."""
        return [
            frame["params"][0]["args"][0]
            for frame in self.sent
            if frame["name"] == "osjs/application:socket:message"
            and frame["params"][0].get("pid") is None
        ]

    def messages(self):
        return [event for event in self.pushes() if event.get("type") == "message"]


@pytest.fixture
def service(app):
    return app.extensions["chat"]


@pytest.fixture
def desktops(app, service):
    """A live websocket for each demo account, synced and subscribed."""
    from server import sockets

    registry = app.extensions["sockets"]
    sessions = {}
    for username in ("demo", "alice", "bob"):
        connection = sockets.Connection(FakeWebsocket(), {"username": username})
        registry.add(connection)
        service.connect(connection.ws)
        sessions[username] = connection

    for connection in sessions.values():
        call(service, connection, op="sync")
    time.sleep(PROPAGATION)
    return sessions


def call(service, connection, **request):
    """One request/response round trip, as the client makes it."""
    replies = []
    service.handle(connection, lambda *args: replies.append(args[0]), [request])
    return replies[0]


def wait_for(predicate, timeout=3):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return False


def test_sync_reports_the_roster_and_who_is_connected(service, desktops):
    reply = call(service, desktops["demo"], op="sync")

    assert reply["me"] == "demo"
    assert [user["username"] for user in reply["users"]] == ["alice", "bob", "demo"]
    assert [room["id"] for room in reply["rooms"]] == ["system"]


def test_a_message_reaches_the_other_member(service, desktops):
    room = call(service, desktops["demo"], op="open", members=["alice"], title="Pair")
    time.sleep(PROPAGATION)

    assert call(service, desktops["demo"], op="send", room=room["id"], body="hello")["seq"] == 1

    assert wait_for(lambda: desktops["alice"].ws.messages())
    delivered = desktops["alice"].ws.messages()[-1]
    assert delivered["body"] == "hello"
    assert delivered["author"] == "demo"
    assert delivered["seq"] == 1


def test_a_message_reaches_nobody_outside_the_room(service, desktops):
    room = call(service, desktops["demo"], op="open", members=["alice"])
    time.sleep(PROPAGATION)
    call(service, desktops["demo"], op="send", room=room["id"], body="private")

    assert wait_for(lambda: desktops["alice"].ws.messages())
    time.sleep(PROPAGATION)
    assert desktops["bob"].ws.messages() == []


def test_a_one_to_one_becomes_a_group_by_adding_a_name(service, desktops):
    """The window's membership is the conversation; nothing is created."""
    room = call(service, desktops["demo"], op="open", members=["alice"])
    time.sleep(PROPAGATION)

    reply = call(service, desktops["demo"], op="invite", room=room["id"], username="bob")
    assert reply["room"]["members"] == ["alice", "bob", "demo"]

    # Bob was not subscribed to a room he was not in, so the news that he is
    # now in one has to arrive on the presence topic.
    assert wait_for(
        lambda: any(
            event.get("type") == "room" and event["room"]["id"] == room["id"]
            for event in desktops["bob"].ws.pushes()
        )
    )


def test_merging_two_windows_unions_their_membership(service, desktops):
    source = call(service, desktops["demo"], op="open", members=["alice"], title="Design")
    target = call(service, desktops["demo"], op="open", members=["bob"], title="Build")
    time.sleep(PROPAGATION)

    reply = call(service, desktops["demo"], op="merge", room=source["id"], into=target["id"])

    assert reply["room"]["members"] == ["alice", "bob", "demo"]

    # History is not renumbered, so both rooms keep their own cursors and each
    # gets a marker instead.
    left = call(service, desktops["demo"], op="history", room=source["id"])
    assert [message["body"] for message in left["messages"]] == ["demo merged this into Build"]


def test_a_non_member_cannot_read_a_room(service, desktops):
    room = call(service, desktops["demo"], op="open", members=["alice"])

    assert call(service, desktops["bob"], op="history", room=room["id"]) == {
        "error": "Not a member of that room"
    }
    assert call(service, desktops["bob"], op="send", room=room["id"], body="hi") == {
        "error": "Not a member of that room"
    }


def test_history_answers_from_a_cursor(service, desktops):
    """The repair path: a client that saw a gap asks for what it missed."""
    room = call(service, desktops["demo"], op="open", members=["alice"])
    for index in range(4):
        call(service, desktops["demo"], op="send", room=room["id"], body=f"m{index}")

    reply = call(service, desktops["demo"], op="history", room=room["id"], since=2)
    assert [message["seq"] for message in reply["messages"]] == [3, 4]


def test_leaving_removes_the_member_and_marks_the_room(service, desktops):
    room = call(service, desktops["demo"], op="open", members=["alice"])
    call(service, desktops["alice"], op="leave", room=room["id"])

    assert call(service, desktops["demo"], op="history", room=room["id"])["messages"][-1][
        "body"
    ] == "alice left"
    assert call(service, desktops["alice"], op="history", room=room["id"]) == {
        "error": "Not a member of that room"
    }


def test_an_unknown_operation_is_refused(service, desktops):
    assert call(service, desktops["demo"], op="drop-tables") == {
        "error": "No such chat operation: drop-tables"
    }


def test_a_filesystem_change_arrives_on_the_system_stream(auth, service, desktops):
    """The machine half: a producer the server owns, in the same window type."""
    from tests.conftest import upload

    assert upload(auth, "home:/note.txt", b"hi").status_code == 200

    assert wait_for(
        lambda: any(
            message["body"] == "demo wrote home:/note.txt"
            for message in desktops["demo"].ws.messages()
        )
    )
    assert desktops["demo"].ws.messages()[-1]["room"] == "system"
