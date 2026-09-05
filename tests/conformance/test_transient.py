"""Transient rooms: the promise that a conversation is not kept.

The only tests here that spend real time. Each launches its own server with a
short grace period, because the deletion is timed and the default is two
minutes.

The tests that assert a room *survives* do not sleep for a fixed interval.
They empty a second room alongside it and wait for that one to go, which proves
a sweep ran past the grace period -- a fixed sleep would only prove the test was
patient.
"""

import time

# A grace short enough to wait out, and a sweep that runs at the same rate. The
# contract puts the deletion between the two.
GRACE_SECONDS = 1
SWEEP_SECONDS = 1
GRACE = {"MINOS_ROOM_GRACE": GRACE_SECONDS, "MINOS_ROOM_SWEEP": SWEEP_SECONDS}

# Ceiling on the grace plus one sweep, with room for a loaded machine.
DELETION_TIMEOUT = 15.0


def gone(room_id):
    return lambda event: event.get("type") == "roomGone" and event.get("room") == room_id


def empty_a_room(socket, title):
    """Raise a transient room, occupy it, and leave. Returns its id."""
    room = socket.call("open", invite=[], retention="transient", title=title)
    occupancy = socket.call("enter", room=room["id"])["occupancy"]
    socket.call("exit", occupancy=occupancy)
    return room["id"]


def await_a_sweep(socket, unique):
    """Block until a sweep has demonstrably run past the grace period.

    A room raised here purely to be deleted. One further sweep interval after
    it goes, so a room deleted just behind it in the same pass has also been
    announced by the time the caller looks.
    """
    doomed = empty_a_room(socket, unique("Doomed"))
    socket.expect_push(gone(doomed), timeout=DELETION_TIMEOUT)
    time.sleep(SWEEP_SECONDS + 1)


def listed(socket):
    return {room["id"] for room in socket.call("sync")["rooms"]}


def test_a_room_is_deleted_once_its_last_occupant_has_been_gone(
    fresh_server, attach, unique
):
    server = fresh_server(**GRACE)
    alice = attach(server, "alice")
    bob = attach(server, "bob")

    room = alice.call("open", invite=["bob"], retention="transient", title=unique("Brief"))
    occupancy = alice.call("enter", room=room["id"])["occupancy"]
    alice.call("send", room=room["id"], body="said and gone")
    alice.call("exit", occupancy=occupancy)

    # Its audience is told before it goes, or it would sit in their lists
    # forever with nothing left to tell them otherwise.
    bob.expect_push(gone(room["id"]), timeout=DELETION_TIMEOUT)

    # Gone, not merely hidden: nothing about it answers any more.
    assert alice.refuse("history", room=room["id"], since=0) == f"No such room: {room['id']}"
    assert room["id"] not in listed(bob)


def test_returning_during_the_grace_period_rescues_the_room(
    fresh_server, attach, unique
):
    """A reload or a dropped connection must not destroy a live conversation."""
    server = fresh_server(**GRACE)
    alice = attach(server, "alice")

    room = alice.call("open", invite=[], retention="transient", title=unique("Rescued"))
    occupancy = alice.call("enter", room=room["id"])["occupancy"]
    alice.call("exit", occupancy=occupancy)
    alice.call("enter", room=room["id"])

    await_a_sweep(alice, unique)
    assert room["id"] in listed(alice)


def test_a_room_nobody_ever_entered_does_not_expire(fresh_server, attach, unique):
    """The countdown starts from emptying, and a room never filled never empties."""
    server = fresh_server(**GRACE)
    alice = attach(server, "alice")

    room = alice.call("open", invite=[], retention="transient", title=unique("Untouched"))

    await_a_sweep(alice, unique)
    assert room["id"] in listed(alice)


def test_a_persisted_room_survives_emptying(fresh_server, attach, unique):
    server = fresh_server(**GRACE)
    alice = attach(server, "alice")

    room = alice.call("open", invite=[], retention="persisted", title=unique("Kept"))
    occupancy = alice.call("enter", room=room["id"])["occupancy"]
    alice.call("exit", occupancy=occupancy)

    await_a_sweep(alice, unique)
    assert room["id"] in listed(alice)


def test_a_dropped_connection_starts_the_countdown(fresh_server, attach, unique):
    """The occupancy is the connection's, so losing one releases it."""
    server = fresh_server(**GRACE)
    alice = attach(server, "alice")
    bob = attach(server, "bob")

    room = alice.call("open", invite=["bob"], retention="transient", title=unique("Dropped"))
    bob.call("enter", room=room["id"])
    bob.close()

    alice.expect_push(gone(room["id"]), timeout=DELETION_TIMEOUT)
