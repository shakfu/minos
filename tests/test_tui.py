"""The terminal client, driven against a real server.

A live HTTP server and a real websocket, because the client's whole job is to
speak the wire format from outside a browser: a mock of the transport would
prove nothing about the thing most likely to be wrong.

The interface itself is not drawn here. What is tested is the layer beneath it
-- the protocol client and its cursors -- plus the command parsing, which is
where a terminal puts the operations the retired interface expressed by
dragging.
"""

import threading
import time

import pytest
from werkzeug.serving import make_server

from tui.protocol import ChatClient, ChatError, connect


@pytest.fixture
def server(app):
    instance = make_server("127.0.0.1", 0, app, threaded=True)
    thread = threading.Thread(target=instance.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{instance.server_port}"
    instance.shutdown()
    thread.join(timeout=5)


@pytest.fixture
def demo(server):
    http, client, profile = connect(server, "demo", "demo")
    yield client
    client.stop()


@pytest.fixture
def alice(server):
    http, client, profile = connect(server, "alice", "alice")
    yield client
    client.stop()


def wait_for(predicate, timeout=5):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return False


# -- the connection -----------------------------------------------------------


def test_login_and_sync_over_a_real_socket(demo):
    assert demo.me == "demo"
    assert demo.is_admin is True
    assert [c["title"] for c in demo.channels.values()] == ["System"]
    assert demo.rooms == {}


def test_an_ordinary_user_is_not_an_admin(alice):
    assert alice.is_admin is False
    with pytest.raises(ChatError):
        alice.create_room("Engineering")


def test_a_bad_password_is_refused_before_curses_starts(server):
    from tui.transport import TransportError

    with pytest.raises(TransportError):
        connect(server, "demo", "wrong")


# -- delivery -----------------------------------------------------------------


def test_a_message_travels_between_two_terminals(demo, alice):
    room = demo.open_room([{"kind": "user", "id": "alice"}], title="Pair")
    time.sleep(0.5)
    demo.send(room["id"], "hello from the terminal")

    assert wait_for(lambda: alice.log.get(room["id"]))
    delivered = alice.log[room["id"]][-1]
    assert delivered["body"] == "hello from the terminal"
    assert delivered["author"] == "demo"


def test_a_gap_is_repaired_rather_than_skipped(demo, alice):
    """The delivery contract, exercised through the real client.

    The cursor is wound back by hand to stand in for messages the bus dropped.
    What matters is that the client notices and asks, rather than rendering the
    new message next to a hole nothing would ever mention.
    """
    room = demo.open_room([{"kind": "user", "id": "alice"}], title="Pair")
    time.sleep(0.5)
    for body in ("one", "two", "three"):
        demo.send(room["id"], body)
    assert wait_for(lambda: len(alice.log.get(room["id"], [])) == 3)

    alice._cursors[room["id"]] = 1
    alice.log[room["id"]] = alice.log[room["id"]][:1]

    demo.send(room["id"], "four")
    assert wait_for(lambda: len(alice.log.get(room["id"], [])) == 4)
    assert [m["body"] for m in alice.log[room["id"]]] == ["one", "two", "three", "four"]


def test_a_shortfall_is_reported_rather_than_hidden(demo, alice):
    """A backfill capped at the tail leaves a gap that will never be filled.

    Closing it silently would defeat the mechanism, so the client says how much
    is missing instead.
    """
    room = demo.open_room([{"kind": "user", "id": "alice"}], title="Pair")
    time.sleep(0.5)
    for index in range(6):
        demo.send(room["id"], str(index))
    assert wait_for(lambda: len(alice.log.get(room["id"], [])) == 6)

    # A client further behind than the server will hand back in one reply.
    alice.log[room["id"]] = []
    alice._cursors[room["id"]] = 0
    alice.client_history_limit = 2
    alice.request = _capped_history(alice.request, limit=2)

    alice._repair(room["id"])
    bodies = [m["body"] for m in alice.log[room["id"]]]
    assert bodies[0] == "4 earlier message(s) not shown"
    assert bodies[1:] == ["4", "5"]


def _capped_history(request, limit):
    """Wrap `request` so a history reply carries only its tail."""

    def wrapped(op, **body):
        reply = request(op, **body)
        if op == "history":
            reply = dict(reply)
            reply["messages"] = reply["messages"][-limit:]
        return reply

    return wrapped


# -- the model, through the client -------------------------------------------


def test_two_rooms_may_hold_the_same_people(demo):
    first = demo.open_room([{"kind": "user", "id": "alice"}], title="One")
    second = demo.open_room([{"kind": "user", "id": "alice"}], title="Two")

    assert first["id"] != second["id"]
    assert first["audience"] == second["audience"]


def test_a_group_can_be_invited_and_tracks_its_membership(demo, alice):
    group = demo.create_group("Team")
    room = demo.create_room("Team room")
    demo.invite(room["id"], {"kind": "group", "id": group["id"]})

    with pytest.raises(ChatError):
        alice.request("history", room=room["id"], since=0)

    demo.assign_group(group["id"], "alice")
    assert "messages" in alice.request("history", room=room["id"], since=0)


def test_entering_a_room_is_not_being_invited_to_it(demo, app):
    room = demo.open_room(title="Meeting")
    timeline = app.extensions["chat"].timeline

    occupancy = demo.enter(room["id"])
    assert timeline.occupants_of(room["id"]) == ["demo"]

    demo.exit(occupancy)
    assert timeline.occupants_of(room["id"]) == []
    assert "messages" in demo.request("history", room=room["id"], since=0)


def test_closing_the_terminal_gives_up_every_room(demo, app, server):
    """Leaving is closing the program, which is what a transient room counts on."""
    room = demo.open_room(title="Standup", retention="transient")
    demo.enter(room["id"])
    timeline = app.extensions["chat"].timeline
    assert timeline.occupants_of(room["id"]) == ["demo"]

    demo.stop()
    assert wait_for(lambda: timeline.occupants_of(room["id"]) == [])


def test_a_channel_is_read_only(demo):
    with pytest.raises(ChatError) as refusal:
        demo.send("system", "can I post here")
    assert "read-only" in str(refusal.value)


def test_the_read_cursor_is_separate_from_the_delivery_cursor(demo):
    room = demo.open_room(title="Notes")
    demo.send(room["id"], "one")
    demo.send(room["id"], "two")

    # Received, and so applied to the delivery cursor.
    assert wait_for(lambda: demo._cursors.get(room["id"]) == 2)
    # But not yet seen: nothing marked it read.
    assert demo.read.get(room["id"], 0) == 0
    assert demo.unread(room["id"]) == 2

    demo.mark_read(room["id"], 2)
    assert demo.unread(room["id"]) == 0


# -- the command layer --------------------------------------------------------


class FakeUi:
    """`Ui`'s command methods without a terminal under them."""

    def __init__(self, client):
        from tui.app import Ui

        self.client = client
        self.selected = None
        self.occupancy = None
        self.notices = []
        self.running = True
        self.dirty = False
        for name in dir(Ui):
            if name.startswith(("cmd_", "principal", "command")):
                setattr(self, name, getattr(Ui, name).__get__(self))

    def notice(self, text):
        self.notices.append(str(text))

    def select(self, space_id):
        self.selected = space_id

    def _release(self):
        self.occupancy = None


def test_a_slash_command_raises_a_room_and_selects_it(demo):
    ui = FakeUi(demo)
    ui.command("/open alice")

    assert ui.selected in demo.rooms
    assert demo.rooms[ui.selected]["audience"] == ["alice", "demo"]


def test_meet_raises_a_transient_room_and_says_so(demo):
    ui = FakeUi(demo)
    ui.command("/meet alice")

    assert demo.rooms[ui.selected]["retention"] == "transient"
    assert any("discarded" in notice for notice in ui.notices)


def test_an_at_prefix_names_a_group(demo):
    demo.create_group("Team", ["alice"])
    ui = FakeUi(demo)
    ui.command("/create Team room")
    ui.command("/invite @Team")

    assert demo.rooms[ui.selected]["audience"] == ["alice", "demo"]


def test_an_unknown_group_is_refused_by_name(demo):
    ui = FakeUi(demo)
    ui.command("/open @nobody")
    assert any("No such group" in notice for notice in ui.notices)


def test_an_unknown_command_suggests_help(demo):
    ui = FakeUi(demo)
    ui.command("/merge")
    assert any("/help" in notice for notice in ui.notices)


def test_a_refusal_from_the_server_reaches_the_notices(alice):
    """An ordinary user cannot found a permanent room, and is told why."""
    ui = FakeUi(alice)
    ui.command("/create Engineering")
    assert any("administrator" in notice for notice in ui.notices)
