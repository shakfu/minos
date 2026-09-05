"""The delivery contract: the one part that survives a change of transport.

The bus drops, duplicates and reorders. A per-room sequence number is what
makes that recoverable, and everything here is about the properties a client's
repair logic relies on.
"""

from .wire import message_in

# The server's backfill cap. A reply this long is a truncation, and `lastSeq`
# is how the client finds out.
HISTORY_LIMIT = 200


def fill(socket, room_id, count):
    """Send `count` messages without waiting for each reply in turn.

    Several requests in flight is what the `pid` is for, and a round trip per
    message would make the two cap tests the slowest in the suite by far.
    """
    pids = [socket.send_op("send", room=room_id, body=str(n)) for n in range(count)]
    return [socket.await_reply(pid)["seq"] for pid in pids]


def test_a_sequence_starts_at_one_and_never_repeats(alice, unique):
    room = alice.call("open", invite=[], title=unique("Seq"))
    seqs = [alice.call("send", room=room["id"], body=str(n))["seq"] for n in range(5)]
    assert seqs == [1, 2, 3, 4, 5]


def test_sequences_are_per_room(alice, unique):
    first = alice.call("open", invite=[], title=unique("A"))
    second = alice.call("open", invite=[], title=unique("B"))

    alice.call("send", room=first["id"], body="one")
    alice.call("send", room=first["id"], body="two")
    assert alice.call("send", room=second["id"], body="one")["seq"] == 1


def test_an_event_consumes_a_sequence_number_too(alice, unique):
    """A client applies both against one cursor, so both must be numbered."""
    room = alice.call("open", invite=[], title=unique("Mixed"))
    alice.call("send", room=room["id"], body="one")
    alice.call("invite", room=room["id"], principal="bob")
    assert alice.call("send", room=room["id"], body="two")["seq"] == 3

    kinds = [m["kind"] for m in alice.call("history", room=room["id"], since=0)["messages"]]
    assert kinds == ["text", "event", "text"]


def test_a_message_is_stored_before_it_is_published(alice, bob, unique):
    """Which is why discarding a message from a gap is safe."""
    room = alice.call("open", invite=["bob"], title=unique("Durable"))
    alice.call("send", room=room["id"], body="hello")

    pushed = bob.expect_push(message_in(room["id"]))
    stored = alice.call("history", room=room["id"], since=pushed["seq"] - 1)["messages"]

    # A push is the stored message plus the `type` that says which push it is.
    assert stored[0] == {key: value for key, value in pushed.items() if key != "type"}


def test_the_room_reports_where_it_ends(alice, unique):
    room = alice.call("open", invite=[], title=unique("End"))
    for n in range(3):
        alice.call("send", room=room["id"], body=str(n))

    reply = alice.call("history", room=room["id"], since=3)
    assert reply["messages"] == []
    assert reply["lastSeq"] == 3


def test_a_cursor_returns_exactly_the_difference(alice, unique):
    """The repair a client makes when it sees a gap."""
    room = alice.call("open", invite=[], title=unique("Gap"))
    for n in range(6):
        alice.call("send", room=room["id"], body=str(n))

    missed = alice.call("history", room=room["id"], since=2)["messages"]
    assert [m["seq"] for m in missed] == [3, 4, 5, 6]
    assert [m["body"] for m in missed] == ["2", "3", "4", "5"]


def test_a_backfill_is_capped_at_the_newest(alice, unique):
    """A client a thousand behind wants the recent ones, not the oldest."""
    room = alice.call("open", invite=[], title=unique("Long"))
    total = HISTORY_LIMIT + 10
    fill(alice, room["id"], total)

    reply = alice.call("history", room=room["id"], since=0)
    assert len(reply["messages"]) == HISTORY_LIMIT
    assert reply["messages"][-1]["seq"] == total
    assert reply["messages"][0]["seq"] == total - HISTORY_LIMIT + 1


def test_a_truncated_backfill_is_detectable_rather_than_pageable(alice, unique):
    """Asking again from the same cursor returns the same slice.

    So the shortfall is not something a client can loop over. It is something
    it must notice: the first sequence returned sits above its cursor, and
    `lastSeq` says where the room really ends.
    """
    room = alice.call("open", invite=[], title=unique("Capped"))
    total = HISTORY_LIMIT + 10
    fill(alice, room["id"], total)

    first = alice.call("history", room=room["id"], since=0)
    again = alice.call("history", room=room["id"], since=0)
    assert first == again

    shortfall = first["messages"][0]["seq"] - first["since"] - 1
    assert shortfall == 10
    assert first["lastSeq"] == total


def test_a_reconnecting_client_repairs_from_its_cursor(alice, connect, unique):
    """The same mechanism as a dropped frame, over a longer absence."""
    room = alice.call("open", invite=["bob"], title=unique("Away"))
    alice.call("send", room=room["id"], body="before")

    returning = connect("bob")
    listed = {r["id"]: r for r in returning.call("sync")["rooms"]}
    assert listed[room["id"]]["lastSeq"] == 1

    alice.call("send", room=room["id"], body="after")
    caught_up = returning.call("history", room=room["id"], since=0)["messages"]
    assert [m["body"] for m in caught_up] == ["before", "after"]
