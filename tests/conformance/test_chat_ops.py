"""Every chat operation: what it answers, and what it refuses.

A refusal is as much a part of the contract as a success -- it is the text a
client puts in front of a user -- so the messages are asserted verbatim.
"""

import pytest

from .wire import WireError

SYSTEM_CHANNEL = "system"


# -- sync -------------------------------------------------------------------


def test_sync_describes_the_whole_of_what_a_client_needs(demo):
    reply = demo.call("sync")
    assert set(reply) == {"me", "isAdmin", "users", "groups", "rooms", "channels", "read"}
    assert reply["me"] == "demo"
    assert reply["isAdmin"] is True


def test_sync_lists_every_account_sorted(alice):
    users = alice.call("sync")["users"]
    assert [user["username"] for user in users] == ["alice", "bob", "demo"]
    assert all(set(user) == {"username", "online"} for user in users)


def test_sync_reports_the_caller_as_online(alice):
    users = {user["username"]: user["online"] for user in alice.call("sync")["users"]}
    assert users["alice"] is True


def test_only_an_administrator_is_told_so(alice):
    assert alice.call("sync")["isAdmin"] is False


def test_the_system_channel_exists_and_everyone_is_in_it(alice):
    channels = {c["id"]: c for c in alice.call("sync")["channels"]}
    system = channels[SYSTEM_CHANNEL]
    assert system["title"] == "System"
    assert system["kind"] == "channel"
    assert system["grants"] == []
    assert "alice" in system["audience"]


def test_an_unknown_operation_names_itself(demo):
    assert demo.refuse("nonsense") == "No such chat operation: nonsense"


# -- raising a room ---------------------------------------------------------


def test_open_raises_an_ad_hoc_room_with_the_creator_in_it(alice):
    room = alice.call("open", invite=["bob"])
    assert room["kind"] == "room"
    assert room["authority"] == "user"
    assert room["retention"] == "persisted"
    assert room["createdBy"] == "alice"
    assert room["audience"] == ["alice", "bob"]
    assert room["occupants"] == []
    assert room["lastSeq"] == 0
    assert sorted(g["id"] for g in room["grants"]) == ["alice", "bob"]


def test_an_unnamed_room_is_titled_after_who_is_in_it(alice):
    assert alice.call("open", invite=["bob"])["title"] == "alice, bob"


def test_a_given_title_is_kept(alice, unique):
    title = unique("Chat")
    assert alice.call("open", invite=["bob"], title=title)["title"] == title


def test_ad_hoc_titles_need_not_be_unique(alice):
    """Two conversations between the same people are two conversations."""
    first = alice.call("open", invite=["bob"], title="Planning")
    second = alice.call("open", invite=["bob"], title="Planning")
    assert first["id"] != second["id"]


def test_a_room_may_be_raised_transient(alice):
    assert alice.call("open", invite=["bob"], retention="transient")["retention"] == "transient"


def test_no_other_retention_exists(alice):
    assert alice.refuse("open", retention="forever") == "No such retention: forever"


def test_open_refuses_an_unknown_invitee(alice):
    assert alice.refuse("open", invite=["nobody"]) == "No such user: nobody"


def test_open_refuses_an_unknown_principal_kind(alice):
    error = alice.refuse("open", invite=[{"kind": "robot", "id": "hal"}])
    assert error == "No such principal kind: robot"


# -- founding a permanent room ----------------------------------------------


def test_create_founds_an_admin_room(demo, unique):
    room = demo.call("create", title=unique("Engineering"), invite=["alice"])
    assert room["authority"] == "admin"
    assert room["retention"] == "persisted"
    assert room["audience"] == ["alice", "demo"]


def test_only_an_administrator_may_found_one(alice, unique):
    assert alice.refuse("create", title=unique("Ops")) == "Only an administrator may do that"


def test_a_permanent_room_needs_a_name(demo):
    assert demo.refuse("create", title="   ") == "A permanent room needs a name"


def test_a_permanent_room_cannot_be_transient(demo, unique):
    """No room is both admin-founded and transient, and asking is not an error.

    An admin who wants a transient room raises it the way anyone does, and gets
    a user-authority room. See chat-concepts.md, core question 2.
    """
    room = demo.call("create", title=unique("Standing"), retention="transient")
    assert room["retention"] == "persisted"
    assert room["authority"] == "admin"


def test_a_permanent_name_is_unique_without_case(demo, unique):
    """"Post it in Engineering" only works if that resolves to one room."""
    title = unique("Engineering")
    demo.call("create", title=title)
    error = demo.refuse("create", title=title.upper())
    assert error == f"A permanent room called {title.upper()!r} already exists"


# -- invitation -------------------------------------------------------------


def test_any_participant_may_invite_to_a_user_room(alice, unique):
    room = alice.call("open", invite=[], title=unique("Ours"))
    reply = alice.call("invite", room=room["id"], principal="bob")
    assert reply["ok"] is True
    assert reply["room"]["audience"] == ["alice", "bob"]


def test_a_participant_may_not_invite_to_an_admin_room(demo, alice, unique):
    room = demo.call("create", title=unique("Board"), invite=["alice"])
    error = alice.refuse("invite", room=room["id"], principal="bob")
    assert error == "Only an administrator may invite to this room"


def test_a_stranger_may_not_invite_to_a_user_room(alice, bob, unique):
    room = alice.call("open", invite=[], title=unique("Private"))
    assert bob.refuse("invite", room=room["id"], principal="bob") == "Not invited to that room"


def test_inviting_twice_changes_nothing(alice, unique):
    room = alice.call("open", invite=["bob"], title=unique("Twice"))
    again = alice.call("invite", room=room["id"], principal="bob")
    assert again["room"]["audience"] == ["alice", "bob"]


def test_uninvite_withdraws_a_grant(alice, unique):
    room = alice.call("open", invite=["bob"], title=unique("Brief"))
    reply = alice.call("uninvite", room=room["id"], principal="bob")
    assert reply["room"]["audience"] == ["alice"]


def test_a_channel_is_not_a_room_to_invite_to(demo):
    assert demo.refuse("invite", room=SYSTEM_CHANNEL, principal="alice") == (
        f"No such room: {SYSTEM_CHANNEL}"
    )


# -- groups as principals ---------------------------------------------------


def test_a_group_grant_admits_its_members(demo, unique):
    group = demo.call("group.create", name=unique("Team"), members=["alice"])
    room = demo.call(
        "create", title=unique("AllHands"), invite=[{"kind": "group", "id": group["id"]}]
    )
    assert room["audience"] == ["alice", "demo"]


def test_a_group_grant_follows_the_group(demo, unique):
    """Assigning someone admits them everywhere the group was invited."""
    group = demo.call("group.create", name=unique("Team"), members=[])
    room = demo.call(
        "create", title=unique("Later"), invite=[{"kind": "group", "id": group["id"]}]
    )
    demo.call("group.assign", group=group["id"], username="bob")

    audience = demo.call("history", room=room["id"], since=0)
    assert audience["room"] == room["id"]
    assert "bob" in demo.call("invite", room=room["id"], principal="bob")["room"]["audience"]


def test_an_unknown_group_cannot_be_invited(demo, unique):
    error = demo.refuse(
        "create", title=unique("Nope"), invite=[{"kind": "group", "id": "absent"}]
    )
    assert error == "No such group: absent"


# -- leaving ----------------------------------------------------------------


def test_a_participant_may_give_up_their_own_grant(alice, bob, unique):
    room = alice.call("open", invite=["bob"], title=unique("Leaving"))
    assert bob.call("leave", room=room["id"]) == {"ok": True}
    assert room["id"] not in {r["id"] for r in bob.call("sync")["rooms"]}


def test_access_from_a_group_cannot_be_left(demo, alice, unique):
    """It would be restored the moment grants were re-evaluated."""
    group = demo.call("group.create", name=unique("Team"), members=["alice"])
    room = demo.call(
        "create", title=unique("Inherited"), invite=[{"kind": "group", "id": group["id"]}]
    )
    error = alice.refuse("leave", room=room["id"])
    assert error == "Access to this room comes from a group, so it cannot be left"


def test_leaving_a_room_one_is_not_in_is_refused(alice, bob, unique):
    room = alice.call("open", invite=[], title=unique("Solo"))
    assert bob.refuse("leave", room=room["id"]) == "Not invited to that room"


# -- occupancy --------------------------------------------------------------


def test_entering_takes_a_place_that_shows_in_the_room(alice, unique):
    room = alice.call("open", invite=[], title=unique("Sit"))
    reply = alice.call("enter", room=room["id"])
    assert reply["ok"] is True
    assert reply["room"] == room["id"]
    assert isinstance(reply["occupancy"], str)

    listed = {r["id"]: r for r in alice.call("sync")["rooms"]}
    assert listed[room["id"]]["occupants"] == ["alice"]


def test_exiting_gives_the_place_up(alice, unique):
    room = alice.call("open", invite=[], title=unique("Stand"))
    occupancy = alice.call("enter", room=room["id"])["occupancy"]
    assert alice.call("exit", occupancy=occupancy) == {"ok": True}

    listed = {r["id"]: r for r in alice.call("sync")["rooms"]}
    assert listed[room["id"]]["occupants"] == []


def test_one_connection_may_not_release_another_s_place(alice, bob, unique):
    """Otherwise a client could end a room somebody else is sitting in."""
    room = alice.call("open", invite=["bob"], title=unique("Shared"))
    occupancy = alice.call("enter", room=room["id"])["occupancy"]
    assert bob.refuse("exit", occupancy=occupancy) == "Not in that room"


def test_entering_a_room_one_may_not_see_is_refused(alice, bob, unique):
    room = alice.call("open", invite=[], title=unique("Closed"))
    assert bob.refuse("enter", room=room["id"]) == "Not invited to that room"


# -- speaking ---------------------------------------------------------------


def test_a_message_is_answered_with_its_sequence(alice, unique):
    room = alice.call("open", invite=[], title=unique("Talk"))
    assert alice.call("send", room=room["id"], body="one") == {"ok": True, "seq": 1}
    assert alice.call("send", room=room["id"], body="two") == {"ok": True, "seq": 2}


def test_an_empty_message_is_refused(alice, unique):
    room = alice.call("open", invite=[], title=unique("Quiet"))
    assert alice.refuse("send", room=room["id"], body="   ") == "Empty message"


def test_a_channel_is_read_only_to_its_audience(alice):
    assert alice.refuse("send", room=SYSTEM_CHANNEL, body="hi") == "A channel is read-only"


def test_speaking_in_a_room_one_may_not_see_is_refused(alice, bob, unique):
    room = alice.call("open", invite=[], title=unique("Sealed"))
    assert bob.refuse("send", room=room["id"], body="hi") == "Not invited to that room"


def test_an_unknown_room_says_so(alice):
    assert alice.refuse("send", room="absent", body="hi") == "No such room: absent"


def test_a_room_id_of_the_wrong_type_is_simply_missing(alice):
    """Not a type error: an id that is not a string is not a room that exists."""
    assert alice.refuse("send", room=5, body="hi") == "No such room: 5"


# -- history ----------------------------------------------------------------


def test_history_returns_everything_after_a_cursor(alice, unique):
    room = alice.call("open", invite=[], title=unique("Log"))
    for body in ("one", "two", "three"):
        alice.call("send", room=room["id"], body=body)

    reply = alice.call("history", room=room["id"], since=1)
    assert reply["room"] == room["id"]
    assert reply["since"] == 1
    assert reply["lastSeq"] == 3
    assert [m["seq"] for m in reply["messages"]] == [2, 3]


def test_a_message_carries_the_whole_shape(alice, unique):
    room = alice.call("open", invite=[], title=unique("Shape"))
    alice.call("send", room=room["id"], body="hello")

    message = alice.call("history", room=room["id"], since=0)["messages"][0]
    assert set(message) == {"room", "seq", "author", "kind", "body", "at"}
    assert message["author"] == "alice"
    assert message["kind"] == "text"
    assert message["body"] == "hello"
    assert message["at"] > 0


def test_an_unparseable_cursor_means_the_beginning(alice, unique):
    room = alice.call("open", invite=[], title=unique("Cursor"))
    alice.call("send", room=room["id"], body="one")

    reply = alice.call("history", room=room["id"], since="nonsense")
    assert reply["since"] == 0
    assert [m["seq"] for m in reply["messages"]] == [1]


def test_an_invitation_is_recorded_as_an_event_in_the_room(alice, unique):
    room = alice.call("open", invite=[], title=unique("Events"))
    alice.call("invite", room=room["id"], principal="bob")

    message = alice.call("history", room=room["id"], since=0)["messages"][-1]
    assert message["kind"] == "event"
    assert message["author"] == "system"
    assert message["body"] == "alice invited bob"


def test_history_of_a_room_one_may_not_see_is_refused(alice, bob, unique):
    room = alice.call("open", invite=[], title=unique("Hidden"))
    assert bob.refuse("history", room=room["id"], since=0) == "Not invited to that room"


# -- read cursors -----------------------------------------------------------


def test_a_read_cursor_is_recorded(alice, unique):
    room = alice.call("open", invite=[], title=unique("Read"))
    alice.call("send", room=room["id"], body="one")
    assert alice.call("read", room=room["id"], seq=1) == {"ok": True, "room": room["id"], "seq": 1}
    assert alice.call("sync")["read"][room["id"]] == 1


def test_a_read_cursor_never_moves_backwards(alice, unique):
    room = alice.call("open", invite=[], title=unique("Rewind"))
    for body in ("one", "two", "three"):
        alice.call("send", room=room["id"], body=body)

    alice.call("read", room=room["id"], seq=3)
    alice.call("read", room=room["id"], seq=1)
    assert alice.call("sync")["read"][room["id"]] == 3


def test_a_read_cursor_is_the_same_fact_from_every_connection(alice, connect, unique):
    room = alice.call("open", invite=[], title=unique("Devices"))
    alice.call("send", room=room["id"], body="one")
    alice.call("read", room=room["id"], seq=1)

    second = connect("alice")
    assert second.call("sync")["read"][room["id"]] == 1


# -- groups -----------------------------------------------------------------


def test_a_group_is_created_with_its_members(demo, unique):
    name = unique("Team")
    group = demo.call("group.create", name=name, members=["alice", "bob"])
    assert set(group) == {"id", "name", "members"}
    assert group["name"] == name
    assert group["members"] == ["alice", "bob"]


def test_unknown_members_are_dropped_rather_than_refused(demo, unique):
    group = demo.call("group.create", name=unique("Team"), members=["alice", "ghost"])
    assert group["members"] == ["alice"]


def test_only_an_administrator_manages_groups(alice, unique):
    assert alice.refuse("group.create", name=unique("Rogue")) == "Only an administrator may do that"


def test_a_group_needs_a_name(demo):
    assert demo.refuse("group.create", name="  ") == "A group needs a name"


def test_assignment_adds_and_unassignment_removes(demo, unique):
    group = demo.call("group.create", name=unique("Team"), members=[])
    assert demo.call("group.assign", group=group["id"], username="bob")["members"] == ["bob"]
    assert demo.call("group.unassign", group=group["id"], username="bob")["members"] == []


def test_assigning_to_an_unknown_group_says_so(demo):
    assert demo.refuse("group.assign", group="absent", username="bob") == "No such group: absent"


def test_assigning_an_unknown_user_says_so(demo, unique):
    group = demo.call("group.create", name=unique("Team"))
    assert demo.refuse("group.assign", group=group["id"], username="ghost") == "No such user: ghost"


# -- presence and occupancy -------------------------------------------------


def test_presence_is_global_and_names_no_room(alice, bob, unique):
    """Two facts, not one. Presence says whether someone could reply."""
    room = alice.call("open", invite=[], title=unique("Alone"))
    alice.call("enter", room=room["id"])

    # bob shares no room with alice and still sees her online.
    online = {user["username"]: user["online"] for user in bob.call("sync")["users"]}
    assert online["alice"] is True


def test_occupancy_is_per_room(alice, unique):
    """And it is the fact a transient room's lifetime is measured from."""
    here = alice.call("open", invite=[], title=unique("Here"))
    elsewhere = alice.call("open", invite=[], title=unique("Elsewhere"))
    alice.call("enter", room=here["id"])

    rooms = {room["id"]: room for room in alice.call("sync")["rooms"]}
    assert rooms[here["id"]]["occupants"] == ["alice"]
    assert rooms[elsewhere["id"]]["occupants"] == []


# -- channels ---------------------------------------------------------------


def test_a_subscription_can_be_dropped_and_retaken(bob):
    assert bob.call("unsubscribe", channel=SYSTEM_CHANNEL) == {"ok": True}
    assert SYSTEM_CHANNEL not in {c["id"] for c in bob.call("sync")["channels"]}

    channel = bob.call("subscribe", channel=SYSTEM_CHANNEL)
    assert "bob" in channel["audience"]


def test_a_room_is_not_a_channel_to_subscribe_to(alice, unique):
    room = alice.call("open", invite=[], title=unique("NotAChannel"))
    assert alice.refuse("subscribe", channel=room["id"]) == f"No such room: {room['id']}"


@pytest.mark.parametrize("operation", ["subscribe", "unsubscribe"])
def test_an_unknown_channel_says_so(alice, operation):
    assert alice.refuse(operation, channel="absent") == "No such room: absent"


# -- the system channel -----------------------------------------------------


def test_a_filesystem_change_is_announced_on_the_system_channel(alice, session, unique):
    name = unique("announced")
    before = alice.call("history", room=SYSTEM_CHANNEL, since=0)["lastSeq"]
    session("alice").upload(f"home:/{name}.txt", b"x")

    messages = alice.call("history", room=SYSTEM_CHANNEL, since=before)["messages"]
    assert any(m["body"] == f"alice wrote home:/{name}.txt" for m in messages)


def test_a_failed_mutation_announces_nothing(alice, session, unique):
    before = alice.call("history", room=SYSTEM_CHANNEL, since=0)["lastSeq"]
    session("alice").vfs("unlink", path=f"home:/{unique('absent')}")

    after = alice.call("history", room=SYSTEM_CHANNEL, since=before)
    assert after["messages"] == []
