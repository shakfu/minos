"""The timeline store: groups, rooms, channels, and what was said in them.

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

Four things in the schema are worth explaining before reading it.

**The high-water mark.** `rooms.high_seq` holds the last sequence a room
issued, rather than the store deriving it from `MAX(seq)` over the messages
still present. Those agree today, because nothing removes a message without
removing its room. They stop agreeing the moment retention does -- a room
trimmed to empty would report zero and reissue numbers a client has already
seen, and the client would judge them stale and silently drop them. Storing the
mark costs one column and is what keeps that door shut.

**Access is a grant, not a member list.** A room's `grants` name principals --
a user or a whole group -- and who may enter is computed from them. A grant to
a group therefore tracks the group: assigning someone to it admits them
everywhere it was invited, and removing them revokes the same. That is the
point of groups, and it is why there is no membership table to fall out of date.

**Occupancy is not access.** `occupants` records who is *in* a room right now,
one row per connection, the same shape as `presence`. A persisted room does not
care. A transient one is defined by it: when its last occupant leaves the room
records the moment in `empty_since`, and the sweep deletes it once the grace
period has passed. That has to be a stored instant rather than a timer, because
a timer would be lost on restart and invisible to the other workers.

**Channels are rooms with a different door.** They share this table, the
sequence and the delivery path, because none of that differs. What differs is
authority -- a subscriber chooses to subscribe and may not write -- so
subscriptions live in their own table rather than sharing `grants` with an
invitation they are not.

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
CREATE TABLE IF NOT EXISTS groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL,
    username TEXT NOT NULL,
    PRIMARY KEY (group_id, username)
);

CREATE TABLE IF NOT EXISTS rooms (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    kind        TEXT NOT NULL,
    authority   TEXT NOT NULL,
    retention   TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  REAL NOT NULL,
    high_seq    INTEGER NOT NULL DEFAULT 0,
    empty_since REAL
);

-- Who was invited. `principal_kind` is 'user' or 'group'; a group grant is
-- resolved through group_members at the moment access is checked, so it
-- follows the group rather than a snapshot of it.
CREATE TABLE IF NOT EXISTS grants (
    room_id        TEXT NOT NULL,
    principal_kind TEXT NOT NULL,
    principal_id   TEXT NOT NULL,
    granted_at     REAL NOT NULL,
    PRIMARY KEY (room_id, principal_kind, principal_id)
);

-- A channel's audience. Separate from grants because it is a different act:
-- chosen by the subscriber rather than granted to them.
CREATE TABLE IF NOT EXISTS subscriptions (
    channel_id TEXT NOT NULL,
    username   TEXT NOT NULL,
    PRIMARY KEY (channel_id, username)
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

-- What each user has read, as opposed to what their client has received. The
-- delivery cursor lives in the client and repairs gaps; this one answers what
-- a person has seen, and is the same fact from every device.
CREATE TABLE IF NOT EXISTS read_cursors (
    room_id  TEXT NOT NULL,
    username TEXT NOT NULL,
    seq      INTEGER NOT NULL,
    PRIMARY KEY (room_id, username)
);

CREATE TABLE IF NOT EXISTS presence (
    id       TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    worker   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS occupants (
    id       TEXT PRIMARY KEY,
    room_id  TEXT NOT NULL,
    username TEXT NOT NULL,
    worker   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS grants_by_principal
    ON grants (principal_kind, principal_id);
CREATE INDEX IF NOT EXISTS occupants_by_room ON occupants (room_id);
"""

# Message kinds. `text` is what a person typed; `event` is what happened to the
# room or, in a channel, to the system.
TEXT = "text"
EVENT = "event"

# What the row is. A channel shares the storage and the sequence; only its door
# differs.
ROOM = "room"
CHANNEL = "channel"

# Who founded it, and therefore who may invite to it.
ADMIN = "admin"
USER = "user"

# Whether it is kept.
PERSISTED = "persisted"
TRANSIENT = "transient"

# Principal kinds a grant can name.
PRINCIPAL_USER = "user"
PRINCIPAL_GROUP = "group"

DEFAULT_HISTORY_LIMIT = 200

# How long a transient room survives having no occupants. Long enough that a
# reload or a dropped connection does not destroy a live conversation, short
# enough that the promise of deletion means something.
DEFAULT_GRACE = 120.0

WORKER_LOCK_PREFIX = "worker-"
WORKER_LOCK_SUFFIX = ".lock"


def encode(payload):
    return json.dumps(payload).encode()


def decode(raw):
    return json.loads(raw.decode())


class Timeline:
    """The durable half of the messaging layer.

    An instance rather than a module of functions, so a host can run more than
    one -- a test suite most obviously -- and so the database path, history cap
    and grace period are given rather than read from somewhere global.
    """

    def __init__(
        self,
        db_path,
        run_dir=None,
        history_limit=DEFAULT_HISTORY_LIMIT,
        grace=DEFAULT_GRACE,
    ):
        self.db_path = Path(db_path)
        # Where the worker liveness locks live. Beside the database by default,
        # since they describe who is currently writing to it.
        self.run_dir = Path(run_dir) if run_dir is not None else self.db_path.parent
        self.history_limit = history_limit
        self.grace = grace

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

    # -- groups ---------------------------------------------------------------

    def create_group(self, name, group_id=None, members=()):
        """Create a group, or return the existing one with that id."""
        group_id = group_id or uuid.uuid4().hex
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT OR IGNORE INTO groups (id, name, created_at) VALUES (?, ?, ?)",
                    (group_id, name, time.time()),
                )
                connection.executemany(
                    "INSERT OR IGNORE INTO group_members (group_id, username) VALUES (?, ?)",
                    [(group_id, username) for username in members],
                )
            return self._describe_group(connection, group_id)

    def _describe_group(self, connection, group_id):
        row = connection.execute("SELECT * FROM groups WHERE id = ?", (group_id,)).fetchone()
        if row is None:
            return None
        members = connection.execute(
            "SELECT username FROM group_members WHERE group_id = ? ORDER BY username",
            (group_id,),
        )
        return {
            "id": row["id"],
            "name": row["name"],
            "members": [member["username"] for member in members],
        }

    def group(self, group_id):
        with self._connect() as connection:
            return self._describe_group(connection, group_id)

    def groups(self):
        with self._connect() as connection:
            rows = connection.execute("SELECT id FROM groups ORDER BY name").fetchall()
            return [self._describe_group(connection, row["id"]) for row in rows]

    def assign_group(self, group_id, username):
        """Add a user to a group. Returns True when it changed anything."""
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "INSERT OR IGNORE INTO group_members (group_id, username) VALUES (?, ?)",
                    (group_id, username),
                )
            return cursor.rowcount > 0

    def unassign_group(self, group_id, username):
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "DELETE FROM group_members WHERE group_id = ? AND username = ?",
                    (group_id, username),
                )
            return cursor.rowcount > 0

    def groups_for(self, username):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT group_id FROM group_members WHERE username = ?", (username,)
            ).fetchall()
            return [row["group_id"] for row in rows]

    # -- rooms ----------------------------------------------------------------

    def create_room(
        self,
        title,
        created_by,
        kind=ROOM,
        authority=USER,
        retention=PERSISTED,
        room_id=None,
        grants=(),
    ):
        """Create a room and return its description.

        A room with an id that already exists is left alone and returned as it
        is, so a well-known channel can be declared on every boot without a
        guard.

        `grants` are `(principal_kind, principal_id)` pairs.
        """
        room_id = room_id or uuid.uuid4().hex
        now = time.time()
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT OR IGNORE INTO rooms"
                    " (id, title, kind, authority, retention, created_by, created_at, high_seq)"
                    " VALUES (?, ?, ?, ?, ?, ?, ?, 0)",
                    (room_id, title, kind, authority, retention, created_by, now),
                )
                connection.executemany(
                    "INSERT OR IGNORE INTO grants"
                    " (room_id, principal_kind, principal_id, granted_at) VALUES (?, ?, ?, ?)",
                    [(room_id, pk, pid, now) for pk, pid in grants],
                )
            return self._describe(connection, room_id)

    def _room_row(self, connection, room_id):
        row = connection.execute("SELECT * FROM rooms WHERE id = ?", (room_id,)).fetchone()
        return None if row is None else dict(row)

    def _grants(self, connection, room_id):
        rows = connection.execute(
            "SELECT principal_kind, principal_id FROM grants WHERE room_id = ?"
            " ORDER BY principal_kind, principal_id",
            (room_id,),
        )
        return [{"kind": row["principal_kind"], "id": row["principal_id"]} for row in rows]

    def _participants(self, connection, room_id):
        """Every user a grant admits, whether named directly or through a group."""
        rows = connection.execute(
            """
            SELECT principal_id AS username FROM grants
             WHERE room_id = ? AND principal_kind = 'user'
            UNION
            SELECT gm.username FROM grants g
              JOIN group_members gm ON gm.group_id = g.principal_id
             WHERE g.room_id = ? AND g.principal_kind = 'group'
             ORDER BY username
            """,
            (room_id, room_id),
        )
        return [row["username"] for row in rows]

    def _subscribers(self, connection, channel_id):
        rows = connection.execute(
            "SELECT username FROM subscriptions WHERE channel_id = ? ORDER BY username",
            (channel_id,),
        )
        return [row["username"] for row in rows]

    def _occupants(self, connection, room_id):
        rows = connection.execute(
            "SELECT DISTINCT username FROM occupants WHERE room_id = ? ORDER BY username",
            (room_id,),
        )
        return [row["username"] for row in rows]

    def _describe(self, connection, room_id):
        room = self._room_row(connection, room_id)
        if room is None:
            return None

        is_channel = room["kind"] == CHANNEL
        audience = (
            self._subscribers(connection, room_id)
            if is_channel
            else self._participants(connection, room_id)
        )
        return {
            "id": room["id"],
            "title": room["title"],
            "kind": room["kind"],
            "authority": room["authority"],
            "retention": room["retention"],
            "createdBy": room["created_by"],
            "createdAt": room["created_at"],
            "grants": [] if is_channel else self._grants(connection, room_id),
            "audience": audience,
            "occupants": self._occupants(connection, room_id),
            # The stored mark, not MAX(seq): see the module docstring.
            "lastSeq": room["high_seq"],
        }

    def room(self, room_id):
        with self._connect() as connection:
            return self._describe(connection, room_id)

    def room_named(self, title, authority=ADMIN, kind=ROOM):
        """An existing room of this authority with this name, if there is one.

        Compared without case, because the name exists to be *referred to* --
        "post it in Engineering" has to resolve to one room, and two that differ
        only in capitalisation would not help anybody telling them apart.
        """
        with self._connect() as connection:
            row = connection.execute(
                "SELECT id FROM rooms"
                " WHERE kind = ? AND authority = ? AND lower(title) = lower(?)",
                (kind, authority, str(title)),
            ).fetchone()
            return None if row is None else self._describe(connection, row["id"])

    def audience(self, room_id):
        """Who a message in this room should be delivered to."""
        with self._connect() as connection:
            room = self._room_row(connection, room_id)
            if room is None:
                return []
            if room["kind"] == CHANNEL:
                return self._subscribers(connection, room_id)
            return self._participants(connection, room_id)

    def has_access(self, room_id, username):
        """True when a grant admits this user, directly or through a group."""
        if not isinstance(room_id, str):
            return False
        with self._connect() as connection:
            room = self._room_row(connection, room_id)
            if room is None:
                return False
            if room["kind"] == CHANNEL:
                row = connection.execute(
                    "SELECT 1 FROM subscriptions WHERE channel_id = ? AND username = ?",
                    (room_id, username),
                ).fetchone()
                return row is not None
            row = connection.execute(
                """
                SELECT 1 FROM grants
                 WHERE room_id = ? AND principal_kind = 'user' AND principal_id = ?
                UNION ALL
                SELECT 1 FROM grants g
                  JOIN group_members gm ON gm.group_id = g.principal_id
                 WHERE g.room_id = ? AND g.principal_kind = 'group' AND gm.username = ?
                 LIMIT 1
                """,
                (room_id, username, room_id, username),
            ).fetchone()
            return row is not None

    def rooms_for(self, username):
        """Every room this user may enter, most recently active first."""
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT r.id
                  FROM rooms r
                  LEFT JOIN messages msg ON msg.room_id = r.id
                 WHERE r.kind = 'room' AND (
                       EXISTS (SELECT 1 FROM grants
                                WHERE room_id = r.id AND principal_kind = 'user'
                                  AND principal_id = ?)
                    OR EXISTS (SELECT 1 FROM grants g
                                 JOIN group_members gm ON gm.group_id = g.principal_id
                                WHERE g.room_id = r.id AND g.principal_kind = 'group'
                                  AND gm.username = ?))
                 GROUP BY r.id
                 ORDER BY COALESCE(MAX(msg.at), r.created_at) DESC
                """,
                (username, username),
            ).fetchall()
            return [self._describe(connection, row["id"]) for row in rows]

    def channels_for(self, username):
        with self._connect() as connection:
            rows = connection.execute(
                """
                SELECT r.id
                  FROM rooms r
                  JOIN subscriptions s ON s.channel_id = r.id
                  LEFT JOIN messages msg ON msg.room_id = r.id
                 WHERE r.kind = 'channel' AND s.username = ?
                 GROUP BY r.id
                 ORDER BY COALESCE(MAX(msg.at), r.created_at) DESC
                """,
                (username,),
            ).fetchall()
            return [self._describe(connection, row["id"]) for row in rows]

    def delete_room(self, room_id):
        """Remove a room and everything that referred to it."""
        with self._connect() as connection:
            with self._write(connection):
                for statement, table in (
                    ("DELETE FROM messages WHERE room_id = ?", None),
                    ("DELETE FROM grants WHERE room_id = ?", None),
                    ("DELETE FROM subscriptions WHERE channel_id = ?", None),
                    ("DELETE FROM read_cursors WHERE room_id = ?", None),
                    ("DELETE FROM occupants WHERE room_id = ?", None),
                    ("DELETE FROM rooms WHERE id = ?", None),
                ):
                    connection.execute(statement, (room_id,))
        return True

    # -- grants and subscriptions ---------------------------------------------

    def add_grant(self, room_id, principal_kind, principal_id):
        """Invite a principal. Returns True when it was not already invited."""
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "INSERT OR IGNORE INTO grants"
                    " (room_id, principal_kind, principal_id, granted_at) VALUES (?, ?, ?, ?)",
                    (room_id, principal_kind, principal_id, time.time()),
                )
            return cursor.rowcount > 0

    def remove_grant(self, room_id, principal_kind, principal_id):
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "DELETE FROM grants WHERE room_id = ?"
                    " AND principal_kind = ? AND principal_id = ?",
                    (room_id, principal_kind, principal_id),
                )
            return cursor.rowcount > 0

    def subscribe(self, channel_id, username):
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "INSERT OR IGNORE INTO subscriptions (channel_id, username) VALUES (?, ?)",
                    (channel_id, username),
                )
            return cursor.rowcount > 0

    def unsubscribe(self, channel_id, username):
        with self._connect() as connection:
            with self._write(connection):
                cursor = connection.execute(
                    "DELETE FROM subscriptions WHERE channel_id = ? AND username = ?",
                    (channel_id, username),
                )
            return cursor.rowcount > 0

    # -- messages -------------------------------------------------------------

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

    def append(self, room_id, author, body, kind=TEXT):
        """Store one message and return it, sequence number included.

        The read of the high-water mark and the insert that depends on it are
        one immediate transaction, so concurrent appends to a room cannot be
        handed the same number. The mark is advanced rather than recomputed:
        it is the room's own count of what it has ever issued, which is what
        keeps it correct if anything ever removes a message.
        """
        with self._connect() as connection:
            with self._write(connection):
                row = connection.execute(
                    "SELECT high_seq FROM rooms WHERE id = ?", (room_id,)
                ).fetchone()
                if row is None:
                    raise KeyError(room_id)
                seq = row["high_seq"] + 1
                at = time.time()
                connection.execute("UPDATE rooms SET high_seq = ? WHERE id = ?", (seq, room_id))
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

    # -- read state -----------------------------------------------------------

    def mark_read(self, room_id, username, seq):
        """Advance a user's read cursor. Never moves it backwards."""
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT INTO read_cursors (room_id, username, seq) VALUES (?, ?, ?)"
                    " ON CONFLICT (room_id, username)"
                    " DO UPDATE SET seq = MAX(seq, excluded.seq)",
                    (room_id, username, int(seq)),
                )
        return True

    def read_cursors(self, username):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT room_id, seq FROM read_cursors WHERE username = ?", (username,)
            )
            return {row["room_id"]: row["seq"] for row in rows}

    # -- presence and occupancy -----------------------------------------------

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

    def online(self):
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT DISTINCT username FROM presence ORDER BY username"
            )
            return [row["username"] for row in rows]

    def enter(self, room_id, username, worker):
        """Record that a user is present in a room. Returns the occupancy id.

        Entering clears `empty_since`: a transient room with somebody in it is
        not counting down, and a re-entry during the grace period is exactly the
        case that must cancel the deletion.
        """
        occupancy_id = uuid.uuid4().hex
        with self._connect() as connection:
            with self._write(connection):
                connection.execute(
                    "INSERT INTO occupants (id, room_id, username, worker) VALUES (?, ?, ?, ?)",
                    (occupancy_id, room_id, username, worker),
                )
                connection.execute(
                    "UPDATE rooms SET empty_since = NULL WHERE id = ?", (room_id,)
                )
        return occupancy_id

    def exit(self, occupancy_id):
        """Drop one occupancy. Returns the room it was in, or None.

        When it was the last one, the room records the instant it emptied. That
        is a stored fact rather than a timer because the sweep that acts on it
        may run in a different process, or after a restart.
        """
        with self._connect() as connection:
            with self._write(connection):
                row = connection.execute(
                    "SELECT room_id FROM occupants WHERE id = ?", (occupancy_id,)
                ).fetchone()
                if row is None:
                    return None
                room_id = row["room_id"]
                connection.execute("DELETE FROM occupants WHERE id = ?", (occupancy_id,))
                self._mark_if_empty(connection, room_id)
            return room_id

    def _mark_if_empty(self, connection, room_id):
        remaining = connection.execute(
            "SELECT 1 FROM occupants WHERE room_id = ? LIMIT 1", (room_id,)
        ).fetchone()
        if remaining is None:
            connection.execute(
                "UPDATE rooms SET empty_since = ? WHERE id = ? AND empty_since IS NULL",
                (time.time(), room_id),
            )

    def occupants_of(self, room_id):
        with self._connect() as connection:
            return self._occupants(connection, room_id)

    # -- sweeps ---------------------------------------------------------------

    def expired_transient_rooms(self, now=None):
        """Transient rooms whose grace period has run out."""
        now = time.time() if now is None else now
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT id FROM rooms"
                " WHERE kind = 'room' AND retention = 'transient'"
                "   AND empty_since IS NOT NULL AND ? - empty_since >= ?",
                (now, self.grace),
            ).fetchall()
            return [row["id"] for row in rows]

    def sweep_transient(self, now=None):
        """Delete transient rooms that have been empty for longer than the grace.

        Returns the ids removed, so the caller can tell their last participants
        the room is gone rather than leaving it in every client's list.
        """
        expired = self.expired_transient_rooms(now)
        for room_id in expired:
            self.delete_room(room_id)
        if expired:
            logger.info("Swept %d expired transient room(s)", len(expired))
        return expired

    def clear_worker(self, worker):
        """Drop a worker's presence and occupancy. Called when a worker stops.

        Rooms that this worker's occupants were the last of start their grace
        period here, so a killed worker cannot leave a transient room alive
        forever with phantom occupants in it.
        """
        with self._connect() as connection:
            with self._write(connection):
                rooms = connection.execute(
                    "SELECT DISTINCT room_id FROM occupants WHERE worker = ?", (worker,)
                ).fetchall()
                connection.execute("DELETE FROM presence WHERE worker = ?", (worker,))
                connection.execute("DELETE FROM occupants WHERE worker = ?", (worker,))
                for row in rooms:
                    self._mark_if_empty(connection, row["room_id"])

    # -- worker liveness ------------------------------------------------------
    #
    # Presence is a row per connection rather than a heartbeat, so a worker
    # killed outright leaves its rows behind and the roster shows phantoms
    # forever. Clearing them needs a liveness signal, and the kernel already
    # provides one: a POSIX lock is dropped when its holder dies. Each worker
    # holds a lock beside the database for as long as it runs, and a starting
    # worker sweeps the locks it can take -- which are exactly the ones whose
    # holder is gone. Same reasoning as the bus broker's claim.

    def worker_lock_path(self, worker):
        return self.run_dir / f"{WORKER_LOCK_PREFIX}{worker}{WORKER_LOCK_SUFFIX}"

    def lease(self, worker=None):
        """A claim on this process's own presence rows."""
        return WorkerLease(self, worker)

    def sweep_dead_workers(self):
        """Clear presence and occupancy left behind by workers no longer running.

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
