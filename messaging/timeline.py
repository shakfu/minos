"""The timeline store: rooms, who is in them, and what was said.

A room is an append-only sequence of messages, and every message carries a
`seq` that is monotonic within its room. That number is the whole delivery
contract: the bus is fire-and-forget, so a client that sees a gap between the
last sequence it holds and the one that just arrived asks for the difference.
Nothing else detects a dropped, duplicated or out-of-order frame.

The sequence therefore cannot be assigned by a publisher -- several worker
processes publish concurrently -- so it is assigned here, inside the same
transaction that stores the row. SQLite is the store for exactly that reason;
a JSON file would need locking to get the same guarantee and would still race
on read-modify-write.

Chats and machine streams are the same object. A stream is a room whose
messages happen to come from a producer rather than a person, which is what
lets a window show either one.

Room and message dictionaries carry `lastSeq` rather than `last_seq`: they are
returned to callers and published on the bus unchanged, so the field names are
part of the wire contract rather than an internal convention.
"""

import contextlib
import json
import logging
import sqlite3
import time
import uuid
from pathlib import Path

try:
    import fcntl
except ImportError:  # pragma: no cover - POSIX only, and this is Linux
    fcntl = None

logger = logging.getLogger(__name__)

SCHEMA = """
CREATE TABLE IF NOT EXISTS rooms (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    kind       TEXT NOT NULL,
    created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS members (
    room_id  TEXT NOT NULL,
    username TEXT NOT NULL,
    PRIMARY KEY (room_id, username)
);

CREATE TABLE IF NOT EXISTS messages (
    room_id TEXT NOT NULL,
    seq     INTEGER NOT NULL,
    author  TEXT NOT NULL,
    kind    TEXT NOT NULL,
    body    TEXT NOT NULL,
    at      REAL NOT NULL,
    PRIMARY KEY (room_id, seq)
);

CREATE TABLE IF NOT EXISTS presence (
    id       TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    worker   TEXT NOT NULL
);
"""

# Message kinds. `text` is what a person typed; `event` is what happened to the
# room or, in a stream, to the system.
TEXT = "text"
EVENT = "event"

# Room kinds.
CHAT = "chat"
STREAM = "stream"

DEFAULT_HISTORY_LIMIT = 200

WORKER_LOCK_PREFIX = "worker-"
WORKER_LOCK_SUFFIX = ".lock"


def encode(payload):
    return json.dumps(payload).encode()


def decode(raw):
    return json.loads(raw.decode())


class Timeline:
    """The durable half of the messaging layer.

    An instance rather than a module of functions, so a host can run more than
    one -- a test suite most obviously, but also a process serving two
    independent conversations -- and so the database path and history cap are
    given rather than read from somewhere global.
    """

    def __init__(self, db_path, run_dir=None, history_limit=DEFAULT_HISTORY_LIMIT):
        self.db_path = Path(db_path)
        # Where the worker liveness locks live. Beside the database by default,
        # since they describe who is currently writing to it.
        self.run_dir = Path(run_dir) if run_dir is not None else self.db_path.parent
        self.history_limit = history_limit

    # -- connections ----------------------------------------------------------

    @contextlib.contextmanager
    def _connect(self):
        """One connection per call.

        SQLite connections cannot be shared between threads and a host may run
        a thread per client, so a pooled or instance-level connection would have
        to be thread-local anyway. WAL makes the open cheap enough that it is
        not worth the bookkeeping.
        """
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        connection = sqlite3.connect(self.db_path, timeout=10, isolation_level=None)
        connection.row_factory = sqlite3.Row
        try:
            yield connection
        finally:
            connection.close()

    @contextlib.contextmanager
    def _write(self, connection):
        """A write transaction that takes the lock up front.

        BEGIN IMMEDIATE, so two workers appending to the same room serialise
        here rather than one of them losing a deferred transaction to
        SQLITE_BUSY after it has already read the sequence number.
        """
        connection.execute("BEGIN IMMEDIATE")
        try:
            yield connection
        except Exception:
            connection.execute("ROLLBACK")
            raise
        connection.execute("COMMIT")

    def init(self):
        with self._connect() as connection:
            connection.execute("PRAGMA journal_mode=WAL")
            connection.executescript(SCHEMA)
        return self

    # -- reads ----------------------------------------------------------------

    def _room_row(self, connection, room_id):
        row = connection.execute("SELECT * FROM rooms WHERE id = ?", (room_id,)).fetchone()
        return None if row is None else dict(row)

    def _members(self, connection, room_id):
        rows = connection.execute(
            "SELECT username FROM members WHERE room_id = ? ORDER BY username", (room_id,)
        )
        return [row["username"] for row in rows]

    def _last_seq(self, connection, room_id):
        row = connection.execute(
            "SELECT COALESCE(MAX(seq), 0) AS seq FROM messages WHERE room_id = ?", (room_id,)
        ).fetchone()
        return row["seq"]

    def _describe(self, connection, room_id):
        room = self._room_row(connection, room_id)
        if room is None:
            return None
        room["members"] = self._members(connection, room_id)
        room["lastSeq"] = self._last_seq(connection, room_id)
        return room

    def room(self, room_id):
        """The room, its membership and its last sequence, or None."""
        with self._connect() as connection:
            return self._describe(connection, room_id)

    def rooms_for(self, username):
        """Every room the user belongs to, most recently active first."""
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT r.id
                FROM rooms r
                JOIN members m ON m.room_id = r.id
                LEFT JOIN messages msg ON msg.room_id = r.id
                WHERE m.username = ?
                GROUP BY r.id
                ORDER BY COALESCE(MAX(msg.at), r.created_at) DESC
                """,
                (username,),
            ).fetchall()
            return [self._describe(connection, row["id"]) for row in rows]

    def is_member(self, room_id, username):
        with self._connect() as connection:
            row = connection.execute(
                "SELECT 1 FROM members WHERE room_id = ? AND username = ?", (room_id, username)
            ).fetchone()
            return row is not None

    def history(self, room_id, since=0, limit=None):
        """Messages after `since`, oldest first.

        Capped at the tail rather than the head: a client that has been away for
        a thousand messages wants the recent ones, not the oldest thousand.

        That cap is not a window a caller can page through -- asking again from
        the same cursor returns the same slice -- so a reply short of the room's
        end is a gap that will not be filled. It is detectable, which is the
        point: the first sequence returned sits above the caller's cursor, and
        the room's `lastSeq` says where the room actually ends.
        """
        limit = min(max(1, limit or self.history_limit), self.history_limit)
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT * FROM (
                    SELECT * FROM messages
                    WHERE room_id = ? AND seq > ?
                    ORDER BY seq DESC
                    LIMIT ?
                ) ORDER BY seq ASC
                """,
                (room_id, since, limit),
            ).fetchall()
            return [
                {
                    "room": row["room_id"],
                    "seq": row["seq"],
                    "author": row["author"],
                    "kind": row["kind"],
                    "body": row["body"],
                    "at": row["at"],
                }
                for row in rows
            ]

    # -- writes ---------------------------------------------------------------

    def create_room(self, title, members, kind=CHAT, room_id=None):
        """Create a room and return its description.

        A room with an id that already exists is left alone and returned as it
        is, so a well-known stream can be declared on every boot without a
        guard.
        """
        room_id = room_id or uuid.uuid4().hex
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT OR IGNORE INTO rooms (id, title, kind, created_at) VALUES (?, ?, ?, ?)",
                    (room_id, title, kind, time.time()),
                )
                connection.executemany(
                    "INSERT OR IGNORE INTO members (room_id, username) VALUES (?, ?)",
                    [(room_id, username) for username in members],
                )
            return self._describe(connection, room_id)

    def add_members(self, room_id, usernames):
        """Add members, returning the ones that were not there already."""
        with self._connect() as connection:
            with self._write(connection):
                existing = set(self._members(connection, room_id))
                added = [name for name in usernames if name not in existing]
                connection.executemany(
                    "INSERT OR IGNORE INTO members (room_id, username) VALUES (?, ?)",
                    [(room_id, name) for name in added],
                )
            return added

    def remove_member(self, room_id, username):
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "DELETE FROM members WHERE room_id = ? AND username = ?", (room_id, username)
                )
            return cursor.rowcount > 0

    def append(self, room_id, author, body, kind=TEXT):
        """Store one message and return it, sequence number included.

        The read of MAX(seq) and the insert that depends on it are one immediate
        transaction, so concurrent appends to a room cannot be handed the same
        number.
        """
        with self._connect() as connection:
            with self._write(connection):
                seq = self._last_seq(connection, room_id) + 1
                at = time.time()
                connection.execute(
                    "INSERT INTO messages (room_id, seq, author, kind, body, at)"
                    " VALUES (?, ?, ?, ?, ?, ?)",
                    (room_id, seq, author, kind, body, at),
                )
        return {
            "room": room_id,
            "seq": seq,
            "author": author,
            "kind": kind,
            "body": body,
            "at": at,
        }

    # -- presence -------------------------------------------------------------

    def arrive(self, username, worker):
        """Record a live connection. Returns its presence id."""
        presence_id = uuid.uuid4().hex
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT INTO presence (id, username, worker) VALUES (?, ?, ?)",
                    (presence_id, username, worker),
                )
        return presence_id

    def depart(self, presence_id):
        with self._connect() as connection:
            with self._write(connection):
                connection.execute("DELETE FROM presence WHERE id = ?", (presence_id,))

    def clear_worker(self, worker):
        """Drop a worker's presence rows. Called when a worker stops."""
        with self._connect() as connection:
            with self._write(connection):
                connection.execute("DELETE FROM presence WHERE worker = ?", (worker,))

    def online(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT DISTINCT username FROM presence ORDER BY username"
            )
            return [row["username"] for row in rows]

    # -- worker liveness ------------------------------------------------------
    #
    # Presence is a row per connection rather than a heartbeat, so a worker
    # killed outright leaves its rows behind and the roster shows phantoms
    # forever. Clearing them needs a liveness signal, and the kernel already
    # provides one: a POSIX lock is dropped when its holder dies. Each worker
    # holds a lock beside the database for as long as it runs, and a starting
    # worker sweeps the locks it can take -- which are exactly the ones whose
    # holder is gone. Same reasoning as the bus broker's claim, for the same
    # reason: a leftover file cannot be told from a live peer, but a lock can.

    def worker_lock_path(self, worker):
        return self.run_dir / f"{WORKER_LOCK_PREFIX}{worker}{WORKER_LOCK_SUFFIX}"

    def lease(self, worker=None):
        """A claim on this process's own presence rows."""
        return WorkerLease(self, worker)

    def sweep_dead_workers(self):
        """Clear presence rows left behind by workers no longer running.

        A lock this process can take is one nobody holds, so its worker is gone.
        Two workers starting at once may sweep the same dead one; the delete is
        idempotent, so the race costs a duplicated statement and nothing else.
        """
        if fcntl is None:  # pragma: no cover - POSIX only
            return []

        reclaimed = []
        pattern = f"{WORKER_LOCK_PREFIX}*{WORKER_LOCK_SUFFIX}"
        for path in sorted(self.run_dir.glob(pattern)):
            worker = path.name[len(WORKER_LOCK_PREFIX) : -len(WORKER_LOCK_SUFFIX)]
            try:
                handle = open(path, "r+")
            except OSError:  # pragma: no cover - swept by someone else
                continue

            with handle:
                try:
                    fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except OSError:
                    continue  # Still held: that worker is alive and owns its rows.

                self.clear_worker(worker)
                reclaimed.append(worker)
                try:
                    path.unlink()
                except OSError:  # pragma: no cover - swept by someone else
                    pass

        if reclaimed:
            logger.info("Reclaimed presence for %d stopped worker(s)", len(reclaimed))
        return reclaimed


class WorkerLease:
    """One worker's claim on its own presence rows, held for its lifetime."""

    def __init__(self, timeline, worker=None):
        self.timeline = timeline
        self.worker = worker or uuid.uuid4().hex[:8]
        self._handle = None

    def claim(self):
        """Take the lock naming this worker as live. Call before sweeping."""
        self.timeline.run_dir.mkdir(parents=True, exist_ok=True)
        if fcntl is None:  # pragma: no cover - POSIX only
            return self.worker

        handle = open(self.timeline.worker_lock_path(self.worker), "w")
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:  # pragma: no cover - the id is a fresh uuid
            handle.close()
            raise
        self._handle = handle
        return self.worker

    def release(self):
        """Drop this worker's rows and its lock. Safe to call more than once."""
        try:
            self.timeline.clear_worker(self.worker)
        except Exception:  # pragma: no cover - shutdown path
            logger.debug("Could not clear presence for %s", self.worker, exc_info=True)

        if self._handle is not None:
            try:
                self.timeline.worker_lock_path(self.worker).unlink()
            except OSError:  # pragma: no cover - already gone
                pass
            self._handle.close()
            self._handle = None
