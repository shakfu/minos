"""The timeline store, and the sequence numbers the delivery design rests on."""

import threading

import pytest


@pytest.fixture
def timeline(app):
    from server import timeline as module

    return module


def test_sequence_starts_at_one_and_is_per_room(timeline):
    first = timeline.create_room("First", ["demo"])
    second = timeline.create_room("Second", ["demo"])

    assert timeline.append(first["id"], "demo", "a")["seq"] == 1
    assert timeline.append(first["id"], "demo", "b")["seq"] == 2
    assert timeline.append(second["id"], "demo", "c")["seq"] == 1


def test_concurrent_appends_never_reuse_a_sequence(timeline):
    """The point of the store.

    Several worker processes publish at once, so the number cannot come from a
    publisher. Threads here stand in for those processes; what matters is that
    the read of MAX(seq) and the insert are one transaction.
    """
    room = timeline.create_room("Busy", ["demo"])
    seen = []
    lock = threading.Lock()

    def append(index):
        message = timeline.append(room["id"], "demo", f"message {index}")
        with lock:
            seen.append(message["seq"])

    threads = [threading.Thread(target=append, args=(i,)) for i in range(24)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()

    assert sorted(seen) == list(range(1, 25))


def test_history_returns_only_what_follows_the_cursor(timeline):
    room = timeline.create_room("Room", ["demo"])
    for index in range(5):
        timeline.append(room["id"], "demo", str(index))

    tail = timeline.history(room["id"], since=3)
    assert [message["seq"] for message in tail] == [4, 5]
    assert [message["body"] for message in tail] == ["3", "4"]


def test_history_keeps_the_tail_when_it_has_to_choose(timeline, monkeypatch):
    from server import config

    monkeypatch.setattr(config, "HISTORY_LIMIT", 3)
    room = timeline.create_room("Room", ["demo"])
    for index in range(10):
        timeline.append(room["id"], "demo", str(index))

    assert [message["seq"] for message in timeline.history(room["id"])] == [8, 9, 10]


def test_membership_is_the_room(timeline):
    room = timeline.create_room("Pair", ["demo", "alice"])
    assert room["members"] == ["alice", "demo"]

    assert timeline.add_members(room["id"], ["bob", "alice"]) == ["bob"]
    assert timeline.room(room["id"])["members"] == ["alice", "bob", "demo"]

    assert timeline.remove_member(room["id"], "alice") is True
    assert timeline.is_member(room["id"], "alice") is False


def test_rooms_for_lists_only_the_users_own(timeline):
    mine = timeline.create_room("Mine", ["demo"])
    timeline.create_room("Theirs", ["alice"])

    assert [room["id"] for room in timeline.rooms_for("demo")] == [mine["id"], "system"]


def test_room_description_carries_the_last_sequence(timeline):
    room = timeline.create_room("Room", ["demo"])
    timeline.append(room["id"], "demo", "one")
    timeline.append(room["id"], "demo", "two")

    assert timeline.room(room["id"])["lastSeq"] == 2


def test_presence_is_shared_state_not_a_set_in_memory(timeline):
    first = timeline.arrive("demo", worker="w1")
    timeline.arrive("alice", worker="w2")

    assert timeline.online() == ["alice", "demo"]

    timeline.depart(first)
    assert timeline.online() == ["alice"]

    timeline.clear_worker("w2")
    assert timeline.online() == []


def test_a_stopped_worker_releases_its_presence(timeline):
    """The clean path: a lease dropped on the way out takes its rows with it."""
    lease = timeline.WorkerLease()
    lease.claim()
    timeline.arrive("demo", lease.worker)

    assert timeline.online() == ["demo"]

    lease.release()
    assert timeline.online() == []


def test_a_killed_worker_is_reclaimed_by_the_next_one(timeline):
    """The whole point of the lease.

    A worker killed outright never runs its own cleanup, so its rows outlive it
    and the roster shows a phantom. The kernel drops its lock, though, which is
    how the next worker to start can tell it apart from one still running.
    """
    dead = timeline.WorkerLease()
    dead.claim()
    timeline.arrive("ghost", dead.worker)

    # kill -9: the process is gone, so the lock goes with it. Closing the handle
    # without releasing the lease is exactly that, minus the rows being dropped.
    dead._handle.close()
    dead._handle = None

    assert timeline.online() == ["ghost"]
    assert timeline.sweep_dead_workers() == [dead.worker]
    assert timeline.online() == []


def test_a_sweep_leaves_a_running_worker_alone(timeline):
    """A lock still held is a worker still serving; its roster must survive."""
    alive = timeline.WorkerLease()
    alive.claim()
    timeline.arrive("demo", alive.worker)

    assert timeline.sweep_dead_workers() == []
    assert timeline.online() == ["demo"]

    alive.release()


def test_releasing_a_lease_twice_is_harmless(timeline):
    lease = timeline.WorkerLease()
    lease.claim()
    lease.release()
    lease.release()
