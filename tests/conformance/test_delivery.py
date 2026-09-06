"""Who receives a push, and who does not.

The reply to an operation says it succeeded. These tests are about the other
half: the frames the server sends to people who did not ask for anything.
"""

from .wire import Socket, message_in, push_of

SYSTEM_CHANNEL = "system"


def test_a_message_reaches_the_rest_of_the_room(alice, bob, unique):
    room = alice.call("open", invite=["bob"], title=unique("Heard"))
    bob.drain()
    alice.call("send", room=room["id"], body="hello")

    event = bob.expect_push(message_in(room["id"]))
    assert event["author"] == "alice"
    assert event["body"] == "hello"
    assert event["seq"] == 1


def test_a_speaker_hears_their_own_message(alice, unique):
    room = alice.call("open", invite=[], title=unique("Echo"))
    alice.drain()
    alice.call("send", room=room["id"], body="hello")
    assert alice.expect_push(message_in(room["id"]))["body"] == "hello"


def test_a_message_does_not_reach_outside_its_room(alice, bob, unique):
    """Proved by a message that must arrive, and what overtook it."""
    shared = alice.call("open", invite=["bob"], title=unique("Shared"))
    private = alice.call("open", invite=[], title=unique("Private"))
    bob.drain()

    alice.call("send", room=private["id"], body="secret")
    alice.call("send", room=shared["id"], body="public")

    heard, before = bob.collect_push(message_in(shared["id"]))
    assert heard["body"] == "public"
    assert not any(event.get("room") == private["id"] for event in before)


def test_an_invitation_announces_the_room_to_the_invited(alice, bob, unique):
    """The push that tells a client a room exists at all."""
    room = alice.call("open", invite=[], title=unique("Later"))
    bob.drain()
    alice.call("invite", room=room["id"], principal="bob")

    event = bob.expect_push(
        lambda e: e.get("type") == "room" and e["room"]["id"] == room["id"]
    )
    assert event["room"]["audience"] == ["alice", "bob"]


def test_a_room_push_carries_the_whole_room(alice, bob, unique):
    """A delta would leave a client that missed one permanently behind."""
    room = alice.call("open", invite=[], title=unique("Whole"))
    bob.drain()
    alice.call("invite", room=room["id"], principal="bob")

    pushed = bob.expect_push(lambda e: e.get("type") == "room")["room"]
    assert set(pushed) == {
        "id", "title", "kind", "authority", "retention",
        "createdBy", "createdAt", "grants", "audience", "occupants", "lastSeq",
    }


def test_removal_is_announced_to_the_person_removed(alice, bob, unique):
    """The audience is read before the change, or nobody would tell them."""
    room = alice.call("open", invite=["bob"], title=unique("Removed"))
    alice.call("uninvite", room=room["id"], principal="bob")

    # Matched on the shape rather than the room alone: the announcement of the
    # room's creation is still in flight and would otherwise be taken for this.
    event = bob.expect_push(
        lambda e: e.get("type") == "room"
        and e["room"]["id"] == room["id"]
        and "bob" not in e["room"]["audience"]
    )
    assert event["room"]["audience"] == ["alice"]


def test_entering_a_room_is_announced_to_its_audience(alice, bob, unique):
    room = alice.call("open", invite=["bob"], title=unique("Arrived"))
    alice.call("enter", room=room["id"])

    event = bob.expect_push(
        lambda e: e.get("type") == "room"
        and e["room"]["id"] == room["id"]
        and e["room"]["occupants"]
    )
    assert event["room"]["occupants"] == ["alice"]


def test_unsubscribing_is_announced_as_the_channel_going_away(bob):
    bob.drain()
    bob.call("unsubscribe", channel=SYSTEM_CHANNEL)

    event = bob.expect_push(push_of("roomGone"))
    assert event["room"] == SYSTEM_CHANNEL
    bob.call("subscribe", channel=SYSTEM_CHANNEL)


def test_losing_a_group_takes_its_rooms_away(demo, bob, unique):
    group = demo.call("group.create", name=unique("Team"), members=["bob"])
    room = demo.call(
        "create", title=unique("Grouped"), invite=[{"kind": "group", "id": group["id"]}]
    )
    bob.expect_push(lambda e: e.get("type") == "room" and e["room"]["id"] == room["id"])
    bob.drain()

    demo.call("group.unassign", group=group["id"], username="bob")
    event = bob.expect_push(push_of("roomGone"))
    assert event["room"] == room["id"]


def test_a_group_change_is_announced_to_everyone(alice, demo, unique):
    """Anyone may be invited through a group, so everyone hears about one."""
    alice.drain()
    name = unique("Team")
    demo.call("group.create", name=name, members=[])

    event = alice.expect_push(push_of("group"))
    assert event["group"]["name"] == name


def test_arriving_and_leaving_are_announced_as_presence(alice, server, session):
    alice.drain()
    http = session("bob")
    socket = Socket(server.base, http.cookie_header()).connect()
    socket.handshake()

    arrival = alice.expect_push(
        lambda e: e.get("type") == "presence" and e.get("username") == "bob"
    )
    assert arrival["online"] is True

    socket.close()
    departure = alice.expect_push(
        lambda e: e.get("type") == "presence"
        and e.get("username") == "bob"
        and e.get("online") is False
    )
    assert departure["online"] is False


def test_presence_does_not_come_back_to_its_subject(alice, server, session):
    """It is a fact about a person, and this one already knows it.

    Proved by bob's arrival, which must reach alice: a frame about alice
    herself would have been queued ahead of it.
    """
    http = session("bob")
    socket = Socket(server.base, http.cookie_header()).connect()
    socket.handshake()
    try:
        _, before = alice.collect_push(
            lambda e: e.get("type") == "presence" and e.get("username") == "bob"
        )
    finally:
        socket.close()

    assert not any(event.get("username") == "alice" for event in before)


def test_a_dropped_connection_releases_the_places_it_held(alice, server, session, unique):
    """Or a transient room nobody is in would stay alive forever."""
    room = alice.call("open", invite=["bob"], title=unique("Held"))

    http = session("bob")
    socket = Socket(server.base, http.cookie_header()).connect()
    socket.handshake()
    socket.call("sync")
    socket.call("enter", room=room["id"])

    listed = {r["id"]: r for r in alice.call("sync")["rooms"]}
    assert listed[room["id"]]["occupants"] == ["bob"]

    socket.close()
    alice.expect_push(
        lambda e: e.get("type") == "room"
        and e["room"]["id"] == room["id"]
        and e["room"]["occupants"] == []
    )


def test_a_filesystem_change_reaches_the_system_channel_live(alice, session, unique):
    alice.drain()
    name = unique("live")
    session("alice").upload(f"home:/{name}.txt", b"x")

    event = alice.expect_push(message_in(SYSTEM_CHANNEL))
    assert event["body"] == f"alice wrote home:/{name}.txt"
    assert event["kind"] == "event"
    assert event["author"] == "system"
