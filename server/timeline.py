"""The timeline store: rooms, who is in them, and what was said.

A room is an append-only sequence of messages, and every message carries a
`seq` that is monotonic within its room. That number is the whole delivery
contract: the bus is fire-and-forget, so a client that sees a gap between the
last sequence it holds and the one that just arrived asks for the difference.
Nothing else detects a dropped, duplicated or out-of-order frame.

The sequence therefore cannot be assigned by a publisher -- several worker
processes publish concurrently -- so it is assigned here, inside the same
transaction that stores the row. SQLite is the store for exactly that reason;
a JSON file in the VFS would need locking to get the same guarantee and would
still race on read-modify-write.

Chats and machine streams are the same object. A stream is a room whose
messages happen to come from a producer rather than a person, which is what
lets a window show either one.
"""

import contextlib
import json
import logging
import sqlite3
import time
import uuid

from . import config

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


@contextlib.contextmanager
def _connect():
    """One connection per call.

    SQLite connections cannot be shared between threads and this server runs a
    thread per websocket, so a pooled or module-level connection would have to
    be thread-local anyway. WAL makes the open cheap enough that it is not
    worth the bookkeeping.
    """
    config.TIMELINE_DB.parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(config.TIMELINE_DB, timeout=10, isolation_level=None)
    connection.row_factory = sqlite3.Row
    try:
        yield connection
    finally:
        connection.close()


def init():
    with _connect() as connection:
        connection.execute("PRAGMA journal_mode=WAL")
        connection.executescript(SCHEMA)


@contextlib.contextmanager
def _write(connection):
    """A write transaction that takes the lock up front.

    BEGIN IMMEDIATE, so two workers appending to the same room serialise here
    rather than one of them losing a deferred transaction to SQLITE_BUSY after
    it has already read the sequence number.
    """
    connection.execute("BEGIN IMMEDIATE")
    try:
        yield connection
    except Exception:
        connection.execute("ROLLBACK")
        raise
    connection.execute("COMMIT")


def _room_row(connection, room_id):
    row = connection.execute("SELECT * FROM rooms WHERE id = ?", (room_id,)).fetchone()
    return None if row is None else dict(row)


def _members(connection, room_id):
    rows = connection.execute(
        "SELECT username FROM members WHERE room_id = ? ORDER BY username", (room_id,)
    )
    return [row["username"] for row in rows]


def _last_seq(connection, room_id):
    row = connection.execute(
        "SELECT COALESCE(MAX(seq), 0) AS seq FROM messages WHERE room_id = ?", (room_id,)
    ).fetchone()
    return row["seq"]


def _describe(connection, room_id):
    room = _room_row(connection, room_id)
    if room is None:
        return None
    room["members"] = _members(connection, room_id)
    room["lastSeq"] = _last_seq(connection, room_id)
    return room


def room(room_id):
    """The room, its membership and its last sequence, or None."""
    with _connect() as connection:
        return _describe(connection, room_id)


def create_room(title, members, kind=CHAT, room_id=None):
    """Create a room and return its description.

    A room with an id that already exists is left alone and returned as it is,
    so the system stream can be declared on every boot without a guard.
    """
    room_id = room_id or uuid.uuid4().hex
    with _connect() as connection:
        with _write(connection):
            connection.execute(
                "INSERT OR IGNORE INTO rooms (id, title, kind, created_at) VALUES (?, ?, ?, ?)",
                (room_id, title, kind, time.time()),
            )
            connection.executemany(
                "INSERT OR IGNORE INTO members (room_id, username) VALUES (?, ?)",
                [(room_id, username) for username in members],
            )
        return _describe(connection, room_id)


def rooms_for(username):
    """Every room the user belongs to, most recently active first."""
    with _connect() as connection:
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
        return [_describe(connection, row["id"]) for row in rows]


def is_member(room_id, username):
    with _connect() as connection:
        row = connection.execute(
            "SELECT 1 FROM members WHERE room_id = ? AND username = ?", (room_id, username)
        ).fetchone()
        return row is not None


def add_members(room_id, usernames):
    """Add members, returning the ones that were not there already."""
    with _connect() as connection:
        with _write(connection):
            existing = set(_members(connection, room_id))
            added = [name for name in usernames if name not in existing]
            connection.executemany(
                "INSERT OR IGNORE INTO members (room_id, username) VALUES (?, ?)",
                [(room_id, name) for name in added],
            )
        return added


def remove_member(room_id, username):
    with _connect() as connection:
        with _write(connection):
            cursor = connection.execute(
                "DELETE FROM members WHERE room_id = ? AND username = ?", (room_id, username)
            )
        return cursor.rowcount > 0


def append(room_id, author, body, kind=TEXT):
    """Store one message and return it, sequence number included.

    The read of MAX(seq) and the insert that depends on it are one immediate
    transaction, so concurrent appends to a room cannot be handed the same
    number.
    """
    with _connect() as connection:
        with _write(connection):
            seq = _last_seq(connection, room_id) + 1
            at = time.time()
            connection.execute(
                "INSERT INTO messages (room_id, seq, author, kind, body, at) VALUES (?, ?, ?, ?, ?, ?)",
                (room_id, seq, author, kind, body, at),
            )
    return {"room": room_id, "seq": seq, "author": author, "kind": kind, "body": body, "at": at}


def history(room_id, since=0, limit=None):
    """Messages after `since`, oldest first.

    Capped at the tail rather than the head: a client that has been away for a
    thousand messages wants the recent ones, not the oldest thousand.

    That cap is not a window a caller can page through -- asking again from the
    same cursor returns the same slice -- so a reply short of the room's end is
    a gap that will not be filled. It is detectable, which is the point: the
    first sequence returned sits above the caller's cursor, and `chat._history`
    reports `lastSeq` alongside. The client marks the shortfall in the log
    rather than closing it silently.
    """
    limit = min(limit or config.HISTORY_LIMIT, config.HISTORY_LIMIT)
    with _connect() as connection:
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


def arrive(username, worker):
    """Record a live connection. Returns its presence id."""
    presence_id = uuid.uuid4().hex
    with _connect() as connection:
        with _write(connection):
            connection.execute(
                "INSERT INTO presence (id, username, worker) VALUES (?, ?, ?)",
                (presence_id, username, worker),
            )
    return presence_id


def depart(presence_id):
    with _connect() as connection:
        with _write(connection):
            connection.execute("DELETE FROM presence WHERE id = ?", (presence_id,))


def clear_worker(worker):
    """Drop a worker's presence rows. Called when a worker stops."""
    with _connect() as connection:
        with _write(connection):
            connection.execute("DELETE FROM presence WHERE worker = ?", (worker,))


# -- worker liveness ----------------------------------------------------------
#
# Presence is a row per connection rather than a heartbeat, so a worker killed
# outright leaves its rows behind and the roster shows phantoms forever. Clearing
# them needs a liveness signal, and the kernel already provides one: a POSIX lock
# is dropped when its holder dies. Each worker holds a lock beside the database
# for as long as it runs, and a starting worker sweeps the locks it can take --
# which are exactly the ones whose holder is gone. Same reasoning as the bus
# broker's claim, for the same reason: a leftover file cannot be told from a live
# peer, but a lock can.

WORKER_LOCK_PREFIX = "worker-"
WORKER_LOCK_SUFFIX = ".lock"


def _worker_lock_path(worker):
    return config.RUN_DIR / f"{WORKER_LOCK_PREFIX}{worker}{WORKER_LOCK_SUFFIX}"


class WorkerLease:
    """One worker's claim on its own presence rows, held for its lifetime."""

    def __init__(self, worker=None):
        self.worker = worker or uuid.uuid4().hex[:8]
        self._handle = None

    def claim(self):
        """Take the lock naming this worker as live. Call before sweeping."""
        config.RUN_DIR.mkdir(parents=True, exist_ok=True)
        if fcntl is None:  # pragma: no cover - POSIX only
            return self.worker

        handle = open(_worker_lock_path(self.worker), "w")
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
            clear_worker(self.worker)
        except Exception:  # pragma: no cover - shutdown path
            logger.debug("Could not clear presence for %s", self.worker, exc_info=True)

        if self._handle is not None:
            try:
                _worker_lock_path(self.worker).unlink()
            except OSError:  # pragma: no cover - already gone
                pass
            self._handle.close()
            self._handle = None


def sweep_dead_workers():
    """Clear presence rows left behind by workers that are no longer running.

    A lock this process can take is one nobody holds, so its worker is gone.
    Two workers starting at once may sweep the same dead one; the delete is
    idempotent, so the race costs a duplicated statement and nothing else.
    """
    if fcntl is None:  # pragma: no cover - POSIX only
        return []

    reclaimed = []
    for path in sorted(config.RUN_DIR.glob(f"{WORKER_LOCK_PREFIX}*{WORKER_LOCK_SUFFIX}")):
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

            clear_worker(worker)
            reclaimed.append(worker)
            try:
                path.unlink()
            except OSError:  # pragma: no cover - swept by someone else
                pass

    if reclaimed:
        logger.info("Reclaimed presence for %d stopped worker(s)", len(reclaimed))
    return reclaimed


def online():
    with _connect() as connection:
        rows = connection.execute("SELECT DISTINCT username FROM presence ORDER BY username")
        return [row["username"] for row in rows]


def encode(payload):
    return json.dumps(payload).encode()


def decode(raw):
    return json.loads(raw.decode())
