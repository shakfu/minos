"""Conversations that survive an unreliable bus and more than one process.

Three pieces, and the argument between them is the whole design:

- `Timeline` is the source of truth: rooms, membership, messages and presence in
  SQLite. Every message gets a sequence number that is monotonic within its
  room, assigned inside the transaction that stores it.

- `Bus` is the ZeroMQ leg between processes: a PUB into an XSUB, a SUB from an
  XPUB, and a proxy joining them. It is fire-and-forget by nature -- a
  subscription that has not propagated, a high water mark, or a subscriber that
  went away all lose messages, and none of them report it.

- `Messaging` is the operations, and the part that makes the first two add up.
  It appends, then publishes, then returns; the relay in each process decides
  who is locally connected.

The sequence number is what makes an unreliable bus survivable. A subscriber
keeps a cursor per room and compares: at or below it, already seen; exactly one
above, the next message; higher, something never arrived -- so ask for the
difference. One mechanism covers a dropped frame, a slow joiner and a reconnect
after an hour, which is why the bus is allowed to be lossy at all.

Nothing here knows what is carrying it. Deliveries go out through a callback,
the roster of who exists is supplied by the host, and refusals are
`MessagingError`. Wiring it up:

    from messaging import Bus, Broker, Messaging, Timeline

    timeline = Timeline(db_path="var/timeline.db").init()
    Broker(xsub, xpub, run_dir="var").start()   # first process wins; rest connect
    bus = Bus(xsub, xpub)

    service = Messaging(
        timeline,
        bus,
        deliver=lambda audience, event: ...,    # hand to your own connections
        roster=lambda: {"alice", "bob"},        # who exists, your business
    )
    bus.start(service.on_bus_message)
"""

from .bus import Broker, Bus, PRESENCE_TOPIC, room_topic, run_broker
from .service import (
    GROUP_EVENT,
    MESSAGE,
    PRESENCE,
    ROOM_EVENT,
    ROOM_GONE,
    Messaging,
    MessagingError,
    cursor,
)
from .timeline import (
    ADMIN,
    CHANNEL,
    DEFAULT_GRACE,
    DEFAULT_HISTORY_LIMIT,
    EVENT,
    PERSISTED,
    PRINCIPAL_GROUP,
    PRINCIPAL_USER,
    ROOM,
    TEXT,
    TRANSIENT,
    USER,
    Timeline,
    WorkerLease,
    decode,
    encode,
)

__all__ = [
    "ADMIN",
    "Broker",
    "Bus",
    "CHANNEL",
    "DEFAULT_GRACE",
    "DEFAULT_HISTORY_LIMIT",
    "EVENT",
    "GROUP_EVENT",
    "MESSAGE",
    "Messaging",
    "MessagingError",
    "PERSISTED",
    "PRESENCE",
    "PRESENCE_TOPIC",
    "PRINCIPAL_GROUP",
    "PRINCIPAL_USER",
    "ROOM",
    "ROOM_EVENT",
    "ROOM_GONE",
    "TEXT",
    "TRANSIENT",
    "USER",
    "Timeline",
    "WorkerLease",
    "cursor",
    "decode",
    "encode",
    "room_topic",
    "run_broker",
]
