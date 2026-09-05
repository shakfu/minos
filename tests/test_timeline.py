"""The timeline store: sequence numbers, grants, occupancy and retention."""

import threading
import time

import pytest

from messaging import timeline as timeline_module


@pytest.fixture
def timeline(app):
    """The application's own store, already initialised."""
    return app.extensions["chat"].timeline


def room(timeline, title="Room", **kwargs):
    kwargs.setdefault("created_by", "demo")
    kwargs.setdefault("grants", [("user", "demo")])
    return timeline.create_room(title, **kwargs)


# -- sequence numbers ---------------------------------------------------------


def test_sequence_starts_at_one_and_is_per_room(timeline):
    first = room(timeline, "First")
    second = room(timeline, "Second")

    assert timeline.append(first["id"], "demo", "a")["seq"] == 1
    assert timeline.append(first["id"], "demo", "b")["seq"] == 2
    assert timeline.append(second["id"], "demo", "c")["seq"] == 1


def test_concurrent_appends_never_reuse_a_sequence(timeline):
    """The point of the store.

    Several worker processes publish at once, so the number cannot come from a
    publisher. Threads here stand in for those processes; what matters is that
    the read of the high-water mark and the insert are one transaction.
    """
    busy = room(timeline, "Busy")
    seen = []
    lock = threading.Lock()

    def append():
        for _ in range(10):
            message = timeline.append(busy["id"], "demo", "x")
            with lock:
                seen.append(message["seq"])

    threads = [threading.Thread(target=append) for _ in range(4)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    assert sorted(seen) == list(range(1, 41))


def test_the_high_water_mark_survives_losing_the_messages(timeline):
    """The reason the mark is stored rather than derived from MAX(seq).

    Nothing in the core removes a message without removing its room, so this
    cannot happen yet. It is exactly what retention would do, and a room that
    reissued a sequence a client had already seen would have its new messages
    silently dropped as stale -- so the guarantee is pinned here rather than
    discovered later.
    """
    quiet = room(timeline, "Quiet")
    for _ in range(5):
        timeline.append(quiet["id"], "demo", "x")

    with timeline._connect() as connection:
        connection.execute("DELETE FROM messages WHERE room_id = ?", (quiet["id"],))

    assert timeline.room(quiet["id"])["lastSeq"] == 5
    assert timeline.append(quiet["id"], "demo", "after")["seq"] == 6


def test_history_returns_only_what_follows_the_cursor(timeline):
    talk = room(timeline, "Talk")
    for body in ("a", "b", "c"):
        timeline.append(talk["id"], "demo", body)

    assert [m["body"] for m in timeline.history(talk["id"], since=1)] == ["b", "c"]
    assert timeline.history(talk["id"], since=3) == []


def test_history_keeps_the_tail_when_it_has_to_choose(timeline):
    """A client far behind wants the recent messages, not the oldest ones."""
    talk = room(timeline, "Long")
    for index in range(10):
        timeline.append(talk["id"], "demo", str(index))

    tail = timeline.history(talk["id"], since=0, limit=3)
    assert [m["body"] for m in tail] == ["7", "8", "9"]


# -- grants and groups --------------------------------------------------------


def test_a_grant_to_a_user_admits_exactly_that_user(timeline):
    private = room(timeline, "Private", grants=[("user", "demo")])

    assert timeline.has_access(private["id"], "demo")
    assert not timeline.has_access(private["id"], "alice")


def test_a_grant_to_a_group_follows_the_group(timeline):
    """Why groups exist, and the reason to be careful editing one.

    The grant names the group, not the people in it at the time, so a later
    assignment admits someone with no second invitation -- and an unassignment
    revokes them from every room the group was invited to.
    """
    team = timeline.create_group("Team", members=["alice"])
    shared = room(timeline, "Shared", grants=[("group", team["id"])])

    assert timeline.has_access(shared["id"], "alice")
    assert not timeline.has_access(shared["id"], "bob")

    timeline.assign_group(team["id"], "bob")
    assert timeline.has_access(shared["id"], "bob")

    timeline.unassign_group(team["id"], "alice")
    assert not timeline.has_access(shared["id"], "alice")


def test_the_audience_unions_direct_and_group_grants(timeline):
    team = timeline.create_group("Team", members=["alice", "bob"])
    shared = room(
        timeline, "Shared", grants=[("user", "demo"), ("group", team["id"])]
    )

    assert timeline.room(shared["id"])["audience"] == ["alice", "bob", "demo"]


def test_rooms_for_lists_every_room_a_grant_admits(timeline):
    team = timeline.create_group("Team", members=["alice"])
    direct = room(timeline, "Direct", grants=[("user", "alice")])
    through = room(timeline, "Through a group", grants=[("group", team["id"])])
    room(timeline, "Not hers", grants=[("user", "bob")])

    titles = {r["title"] for r in timeline.rooms_for("alice")}
    assert titles == {direct["title"], through["title"]}


def test_a_channel_answers_to_subscriptions_rather_than_grants(timeline):
    channel = timeline.create_room(
        "News", created_by="system", kind=timeline_module.CHANNEL
    )
    assert not timeline.has_access(channel["id"], "alice")

    timeline.subscribe(channel["id"], "alice")
    assert timeline.has_access(channel["id"], "alice")
    # The application already declares a System channel and subscribes
    # everyone, so this asserts on membership rather than the whole list.
    assert "News" in {c["title"] for c in timeline.channels_for("alice")}

    # A channel is not a room, and does not show up as one.
    assert timeline.rooms_for("alice") == []


# -- occupancy and retention --------------------------------------------------


def test_occupancy_is_not_access(timeline):
    """Being invited and being present are different facts."""
    meeting = room(timeline, "Meeting", grants=[("user", "demo")])

    assert timeline.has_access(meeting["id"], "demo")
    assert timeline.occupants_of(meeting["id"]) == []

    occupancy = timeline.enter(meeting["id"], "demo", worker="w1")
    assert timeline.occupants_of(meeting["id"]) == ["demo"]

    timeline.exit(occupancy)
    assert timeline.occupants_of(meeting["id"]) == []
    assert timeline.has_access(meeting["id"], "demo")


def test_a_transient_room_starts_its_clock_when_the_last_occupant_leaves(timeline):
    meeting = room(timeline, "Standup", retention=timeline_module.TRANSIENT)
    first = timeline.enter(meeting["id"], "demo", worker="w1")
    second = timeline.enter(meeting["id"], "alice", worker="w1")

    timeline.exit(first)
    assert timeline.expired_transient_rooms(now=time.time() + 10_000) == []

    timeline.exit(second)
    assert timeline.expired_transient_rooms(now=time.time() + 10_000) == [meeting["id"]]


def test_re_entering_during_the_grace_period_rescues_the_room(timeline):
    """The whole reason for a grace period: a reload must not destroy a room."""
    meeting = room(timeline, "Standup", retention=timeline_module.TRANSIENT)
    occupancy = timeline.enter(meeting["id"], "demo", worker="w1")
    timeline.exit(occupancy)

    timeline.enter(meeting["id"], "demo", worker="w1")
    assert timeline.expired_transient_rooms(now=time.time() + 10_000) == []


def test_the_sweep_only_takes_rooms_past_the_grace(timeline):
    timeline.grace = 60.0
    meeting = room(timeline, "Standup", retention=timeline_module.TRANSIENT)
    occupancy = timeline.enter(meeting["id"], "demo", worker="w1")
    timeline.exit(occupancy)

    assert timeline.sweep_transient() == []
    assert timeline.sweep_transient(now=time.time() + 61) == [meeting["id"]]
    assert timeline.room(meeting["id"]) is None


def test_a_persisted_room_is_never_swept(timeline):
    kept = room(timeline, "Kept", retention=timeline_module.PERSISTED)
    occupancy = timeline.enter(kept["id"], "demo", worker="w1")
    timeline.exit(occupancy)

    assert timeline.sweep_transient(now=time.time() + 10_000) == []
    assert timeline.room(kept["id"]) is not None


def test_a_swept_room_takes_its_history_with_it(timeline):
    """Transient means transient: nothing is kept, and nothing can rescue it."""
    meeting = room(timeline, "Standup", retention=timeline_module.TRANSIENT)
    timeline.append(meeting["id"], "demo", "said in confidence")
    occupancy = timeline.enter(meeting["id"], "demo", worker="w1")
    timeline.exit(occupancy)

    timeline.sweep_transient(now=time.time() + 10_000)
    assert timeline.history(meeting["id"]) == []
    assert timeline.rooms_for("demo") == []


def test_a_dead_worker_releases_the_rooms_it_was_sitting_in(timeline):
    """A killed worker must not keep a transient room alive with phantoms."""
    meeting = room(timeline, "Standup", retention=timeline_module.TRANSIENT)
    timeline.enter(meeting["id"], "demo", worker="gone")

    assert timeline.expired_transient_rooms(now=time.time() + 10_000) == []
    timeline.clear_worker("gone")
    assert timeline.expired_transient_rooms(now=time.time() + 10_000) == [meeting["id"]]


# -- read state ---------------------------------------------------------------


def test_the_read_cursor_only_moves_forward(timeline):
    talk = room(timeline, "Talk")
    timeline.mark_read(talk["id"], "demo", 5)
    timeline.mark_read(talk["id"], "demo", 2)

    assert timeline.read_cursors("demo") == {talk["id"]: 5}
    assert timeline.read_cursors("alice") == {}
