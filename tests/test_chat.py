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

    def of_type(self, type_):
        return [event for event in self.pushes() if event.get("type") == type_]


@pytest.fixture
def service(app):
    """The websocket-facing handler, which is what the client drives."""
    return app.extensions["chat"]


@pytest.fixture
def clients(app, service):
    """A live websocket per demo account, synced and subscribed.

    demo is in `config.ADMINS`; alice and bob are not. That asymmetry is the
    point of several tests below, so it is set up once here.
    """
    from server import config, sockets

    registry = app.extensions["sockets"]
    sessions = {}
    for username in ("demo", "alice", "bob"):
        profile = {
            "username": username,
            "groups": ["admin"] if username in config.ADMINS else [],
        }
        connection = sockets.Connection(FakeWebsocket(), profile)
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


# -- sync and delivery --------------------------------------------------------


def test_sync_reports_the_roster_groups_and_channels(service, clients):
    reply = call(service, clients["demo"], op="sync")

    assert reply["me"] == "demo"
    assert reply["isAdmin"] is True
    assert [user["username"] for user in reply["users"]] == ["alice", "bob", "demo"]
    # The system channel is a channel, not a room; nobody is invited to it.
    assert reply["rooms"] == []
    assert [channel["id"] for channel in reply["channels"]] == ["system"]


def test_sync_does_not_call_an_ordinary_user_an_admin(service, clients):
    assert call(service, clients["alice"], op="sync")["isAdmin"] is False


def test_a_message_reaches_the_other_participant(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Pair")
    time.sleep(PROPAGATION)

    assert call(service, clients["demo"], op="send", room=room["id"], body="hello")["seq"] == 1

    assert wait_for(lambda: clients["alice"].ws.messages())
    delivered = clients["alice"].ws.messages()[-1]
    assert delivered["body"] == "hello"
    assert delivered["author"] == "demo"
    assert delivered["seq"] == 1


def test_a_message_reaches_nobody_outside_the_room(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"])
    time.sleep(PROPAGATION)
    call(service, clients["demo"], op="send", room=room["id"], body="private")

    assert wait_for(lambda: clients["alice"].ws.messages())
    time.sleep(PROPAGATION)
    assert clients["bob"].ws.messages() == []


def test_someone_not_invited_cannot_read_the_room(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"])

    refusal = call(service, clients["bob"], op="history", room=room["id"], since=0)
    assert "error" in refusal


# -- rooms are places ---------------------------------------------------------


def test_two_rooms_may_hold_the_same_people(service, clients):
    """A room is a place, not a set of people.

    Under the retired model this was impossible by definition, and the client
    had to search for an existing pair before opening one. Now it is ordinary,
    and the two rooms keep separate histories.
    """
    first = call(service, clients["demo"], op="open", invite=["alice"], title="One")
    second = call(service, clients["demo"], op="open", invite=["alice"], title="Two")
    time.sleep(PROPAGATION)

    assert first["id"] != second["id"]
    assert first["audience"] == second["audience"] == ["alice", "demo"]

    call(service, clients["demo"], op="send", room=first["id"], body="only here")
    assert call(service, clients["demo"], op="history", room=second["id"])["messages"] == []


def test_inviting_someone_leaves_the_same_room(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Pair")
    call(service, clients["demo"], op="send", room=room["id"], body="before")

    grown = call(
        service, clients["demo"], op="invite", room=room["id"], principal="bob"
    )["room"]

    assert grown["id"] == room["id"]
    assert grown["audience"] == ["alice", "bob", "demo"]
    # History belongs to the place, so the new participant sees all of it.
    bodies = [
        m["body"]
        for m in call(service, clients["bob"], op="history", room=room["id"])["messages"]
    ]
    assert "before" in bodies


# -- authority ----------------------------------------------------------------


def test_only_an_admin_may_found_a_permanent_room(service, clients):
    refusal = call(service, clients["alice"], op="create", title="Engineering")
    assert "error" in refusal

    room = call(service, clients["demo"], op="create", title="Engineering")
    assert room["authority"] == "admin"
    assert room["retention"] == "persisted"


def test_a_participant_cannot_invite_to_an_admin_room(service, clients):
    """An institutional room's membership is an administrative fact."""
    room = call(service, clients["demo"], op="create", title="Engineering",
                invite=["alice"])
    time.sleep(PROPAGATION)

    refusal = call(
        service, clients["alice"], op="invite", room=room["id"], principal="bob"
    )
    assert "error" in refusal

    allowed = call(
        service, clients["demo"], op="invite", room=room["id"], principal="bob"
    )
    assert "bob" in allowed["room"]["audience"]


def test_any_participant_may_invite_to_a_user_room(service, clients):
    """Permissive on purpose: alice could raise her own room with the same people."""
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Ad hoc")
    time.sleep(PROPAGATION)

    grown = call(
        service, clients["alice"], op="invite", room=room["id"], principal="bob"
    )
    assert "bob" in grown["room"]["audience"]


def test_only_an_admin_may_manage_groups(service, clients):
    assert "error" in call(service, clients["alice"], op="group.create", name="Team")

    group = call(service, clients["demo"], op="group.create", name="Team")
    assert group["name"] == "Team"


# -- groups as principals -----------------------------------------------------


def test_inviting_a_group_admits_its_members(service, clients):
    group = call(
        service, clients["demo"], op="group.create", name="Team", members=["alice"]
    )
    room = call(service, clients["demo"], op="create", title="Team room")
    grown = call(
        service,
        clients["demo"],
        op="invite",
        room=room["id"],
        principal={"kind": "group", "id": group["id"]},
    )["room"]

    assert grown["audience"] == ["alice", "demo"]


def test_a_later_assignment_admits_without_a_second_invitation(service, clients):
    """The grant tracks the group, which is the whole reason groups exist."""
    group = call(service, clients["demo"], op="group.create", name="Team")
    room = call(service, clients["demo"], op="create", title="Team room")
    call(
        service,
        clients["demo"],
        op="invite",
        room=room["id"],
        principal={"kind": "group", "id": group["id"]},
    )

    assert "error" in call(service, clients["bob"], op="history", room=room["id"])

    call(service, clients["demo"], op="group.assign", group=group["id"], username="bob")
    assert "messages" in call(service, clients["bob"], op="history", room=room["id"])


def test_unassigning_from_a_group_revokes_the_rooms_it_carried(service, clients):
    """The other half of the same coin, and the reason to edit a group carefully."""
    group = call(
        service, clients["demo"], op="group.create", name="Team", members=["bob"]
    )
    room = call(service, clients["demo"], op="create", title="Team room")
    call(
        service,
        clients["demo"],
        op="invite",
        room=room["id"],
        principal={"kind": "group", "id": group["id"]},
    )
    assert "messages" in call(service, clients["bob"], op="history", room=room["id"])

    call(
        service, clients["demo"], op="group.unassign", group=group["id"], username="bob"
    )
    assert "error" in call(service, clients["bob"], op="history", room=room["id"])


def test_a_room_reached_through_a_group_cannot_be_left(service, clients):
    """Leaving would be undone the moment the grant was re-evaluated."""
    group = call(
        service, clients["demo"], op="group.create", name="Team", members=["alice"]
    )
    room = call(service, clients["demo"], op="create", title="Team room")
    call(
        service,
        clients["demo"],
        op="invite",
        room=room["id"],
        principal={"kind": "group", "id": group["id"]},
    )

    assert "error" in call(service, clients["alice"], op="leave", room=room["id"])


def test_leaving_gives_up_a_personal_grant(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Pair")
    time.sleep(PROPAGATION)

    assert call(service, clients["alice"], op="leave", room=room["id"])["ok"]
    assert "error" in call(service, clients["alice"], op="history", room=room["id"])


# -- occupancy and retention --------------------------------------------------


def test_entering_and_leaving_a_room_is_not_being_invited_to_it(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Meeting")

    entered = call(service, clients["demo"], op="enter", room=room["id"])
    assert entered["ok"]
    assert service.timeline.occupants_of(room["id"]) == ["demo"]

    call(service, clients["demo"], op="exit", occupancy=entered["occupancy"])
    assert service.timeline.occupants_of(room["id"]) == []
    # Still invited: occupancy ended, access did not.
    assert "messages" in call(service, clients["demo"], op="history", room=room["id"])


def test_a_connection_releases_the_rooms_it_was_sitting_in(service, clients):
    """A dropped socket must not keep a transient room alive forever."""
    room = call(
        service, clients["demo"], op="open", title="Standup", retention="transient"
    )
    call(service, clients["demo"], op="enter", room=room["id"])
    assert service.timeline.occupants_of(room["id"]) == ["demo"]

    service.disconnect(clients["demo"].ws)
    assert service.timeline.occupants_of(room["id"]) == []


def test_one_client_cannot_release_another_clients_place(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Meeting")
    time.sleep(PROPAGATION)
    entered = call(service, clients["demo"], op="enter", room=room["id"])

    refusal = call(
        service, clients["alice"], op="exit", occupancy=entered["occupancy"]
    )
    assert "error" in refusal
    assert service.timeline.occupants_of(room["id"]) == ["demo"]


def test_a_transient_room_is_swept_once_its_grace_has_passed(service, clients):
    room = call(
        service, clients["demo"], op="open", title="Standup", retention="transient"
    )
    call(service, clients["demo"], op="send", room=room["id"], body="said in the room")
    entered = call(service, clients["demo"], op="enter", room=room["id"])
    call(service, clients["demo"], op="exit", occupancy=entered["occupancy"])

    service.timeline.grace = 0.0
    assert service.service.sweep() == [room["id"]]

    assert service.timeline.room(room["id"]) is None
    assert "error" in call(service, clients["demo"], op="history", room=room["id"])


def test_a_swept_room_is_announced_so_clients_can_drop_it(service, clients):
    room = call(
        service, clients["demo"], op="open", title="Standup", retention="transient"
    )
    entered = call(service, clients["demo"], op="enter", room=room["id"])
    call(service, clients["demo"], op="exit", occupancy=entered["occupancy"])

    service.timeline.grace = 0.0
    service.service.sweep()

    assert wait_for(lambda: clients["demo"].ws.of_type("roomGone"))
    assert clients["demo"].ws.of_type("roomGone")[-1]["room"] == room["id"]


# -- channels -----------------------------------------------------------------


def test_a_channel_is_read_only_to_its_audience(service, clients):
    refusal = call(service, clients["alice"], op="send", room="system", body="hello")
    assert refusal["error"] == "A channel is read-only"


def test_the_server_publishes_to_the_system_channel(service, clients):
    service.publish_system_event("demo wrote home:/notes.txt")

    assert wait_for(lambda: clients["alice"].ws.messages())
    assert clients["alice"].ws.messages()[-1]["body"] == "demo wrote home:/notes.txt"


def test_unsubscribing_drops_the_channel(service, clients):
    call(service, clients["alice"], op="unsubscribe", channel="system")
    assert call(service, clients["alice"], op="sync")["channels"] == []


# -- read state ---------------------------------------------------------------


def test_the_read_cursor_is_the_servers_and_comes_back_on_sync(service, clients):
    room = call(service, clients["demo"], op="open", invite=["alice"], title="Pair")
    call(service, clients["demo"], op="send", room=room["id"], body="one")
    call(service, clients["demo"], op="send", room=room["id"], body="two")

    call(service, clients["demo"], op="read", room=room["id"], seq=2)

    assert call(service, clients["demo"], op="sync")["read"][room["id"]] == 2
    # It is per person, not per room.
    assert call(service, clients["alice"], op="sync")["read"] == {}


def test_an_unknown_operation_is_refused_by_name(service, clients):
    assert "No such chat operation" in call(service, clients["demo"], op="merge")["error"]


def test_a_permanent_room_name_is_unique(service, clients):
    """A name exists to be referred to, so two of them help nobody.

    Case is ignored for the same reason: "post it in Engineering" has to resolve
    to one room, and two differing only in capitalisation would not help anyone
    tell them apart.
    """
    call(service, clients["demo"], op="create", title="Engineering")

    refusal = call(service, clients["demo"], op="create", title="engineering")
    assert "already exists" in refusal["error"]


def test_ad_hoc_rooms_may_share_a_title(service, clients):
    """Their title describes who is in them rather than naming them.

    Requiring these to differ would be membership-as-identity coming back: two
    conversations between the same people are two conversations.
    """
    first = call(service, clients["demo"], op="open", invite=["alice"])
    second = call(service, clients["demo"], op="open", invite=["alice"])

    assert first["title"] == second["title"] == "alice, demo"
    assert first["id"] != second["id"]


def test_an_ad_hoc_room_does_not_block_a_permanent_name(service, clients):
    call(service, clients["demo"], op="open", title="Engineering")
    assert "error" not in call(service, clients["demo"], op="create", title="Engineering")


def test_a_channel_is_founded_by_an_admin_and_written_to_by_one(service, clients):
    """The producer's path: the admin publishes, and it is not `send`."""
    channel = call(service, clients["demo"], op="channel.create", title="Announcements")

    assert channel["authority"] == "admin"
    assert channel["createdBy"] == "demo"
    # Founding is not subscribing: subscription is the subscriber's own act.
    assert channel["audience"] == []

    published = call(
        service, clients["demo"], op="channel.publish",
        channel=channel["id"], body="the first",
    )
    assert published == {"ok": True, "seq": 1}

    # Reading is the audience's act, publishing is not, so this needs both.
    call(service, clients["demo"], op="subscribe", channel=channel["id"])
    message = call(
        service, clients["demo"], op="history", room=channel["id"], since=0
    )["messages"][-1]
    assert (message["author"], message["kind"]) == ("demo", "text")


def test_founding_a_channel_is_announced_on_the_system_channel(service, clients):
    """Nobody is subscribed to a new channel, so this is how anyone hears of it."""
    channel = call(service, clients["demo"], op="channel.create", title="Bulletins")
    system = call(service, clients["alice"], op="history", room="system", since=0)

    assert any(channel["id"] in m["body"] for m in system["messages"])


def test_a_channel_name_is_unique_among_channels_only(service, clients):
    """A room and a channel may share a name: they are not the same kind of thing."""
    call(service, clients["demo"], op="channel.create", title="Engineering")
    call(service, clients["demo"], op="create", title="Engineering")

    refused = call(service, clients["demo"], op="channel.create", title="engineering")
    assert refused["error"] == "A channel called 'engineering' already exists"


def test_an_ordinary_user_neither_founds_nor_publishes(service, clients):
    refusal = "Only an administrator may do that"
    assert call(
        service, clients["alice"], op="channel.create", title="Mine"
    )["error"] == refusal
    assert call(
        service, clients["alice"], op="channel.publish", channel="system", body="hi"
    )["error"] == refusal
