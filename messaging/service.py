"""The messaging operations: rooms, membership, presence and streams.

A room here is not a channel someone joined. It is a set of people, and the
membership is the conversation; a one-to-one becomes a group when another name
is added to it, with nothing created and nothing named. Machine streams are the
same object with a producer instead of a person, which is what lets one view
display either.

Nothing in this module knows how its callers are connected. Operations return
plain dictionaries and refusals are exceptions; deliveries go out through a
`deliver(audience, event)` callback the host provides. That is what keeps the
layer independent of whatever transport is carrying it -- a websocket here, but
the module does not know that.

Delivery never consults the connection list. An operation appends to the
timeline, publishes to the bus, and returns; the relay in each process decides
who is locally connected and hands them over. That is what makes a second
worker process work at all.
"""

import logging

from .bus import PRESENCE_TOPIC, room_topic
from .timeline import EVENT, STREAM

logger = logging.getLogger(__name__)

# Push event types, as seen by a subscriber.
MESSAGE = "message"
ROOM = "room"
PRESENCE = "presence"


class MessagingError(Exception):
    """A request that cannot be carried out, reportable to whoever asked.

    Distinct from a bug: the host is expected to catch this and turn it into
    whatever its protocol calls an error reply.
    """


def cursor(value):
    """A caller-supplied cursor, as a non-negative integer.

    Anything unparseable means the caller holds nothing, which is what a cursor
    of zero says. A bad value is not worth an error: the reply is a backfill
    either way, and the caller's own cursor decides what it keeps.
    """
    try:
        return max(0, int(value or 0))
    except (TypeError, ValueError):
        return 0


class Messaging:
    """One process's view of the conversation.

    Owns nothing durable -- the timeline does -- beyond which topics this
    process's subscribers have made it care about.

    `deliver(audience, event)` is called for every message this process should
    hand out: `audience` is the set of usernames it is addressed to, or None for
    everyone. `roster()` returns the usernames that exist, which is the host's
    business rather than this module's.
    """

    def __init__(self, timeline, bus, deliver, roster):
        self.timeline = timeline
        self.bus = bus
        self._deliver = deliver
        self._roster = roster
        self._subscriptions = {}

    # -- delivery -------------------------------------------------------------

    def on_bus_message(self, topic, payload):
        """Hand a bus message to the host. Wire this to `Bus.start`.

        The publisher works out the recipients, so a process never queries
        membership to deliver: the audience travels with the message.
        """
        self._deliver(payload.get("to"), payload.get("event") or {})

    def _publish(self, topic, event, audience):
        self.bus.publish(topic, {"to": sorted(audience), "event": event})

    def _watch(self, subscriber, room_id):
        """Subscribe this process to a room for as long as a subscriber wants it.

        `subscriber` is an opaque key the host chooses -- whatever it can hand
        back on release. Subscriptions are counted in the bus, so two
        subscribers on one room do not cancel each other.
        """
        if subscriber is None:
            return
        topic = room_topic(room_id)
        watched = self._subscriptions.setdefault(subscriber, set())
        if topic not in watched:
            watched.add(topic)
            self.bus.subscribe(topic)

    def connect(self, subscriber):
        """Register a subscriber and start it on the presence topic."""
        self._subscriptions.setdefault(subscriber, set()).add(PRESENCE_TOPIC)
        self.bus.subscribe(PRESENCE_TOPIC)

    def disconnect(self, subscriber):
        """Release everything a subscriber was watching."""
        for topic in self._subscriptions.pop(subscriber, ()):
            self.bus.unsubscribe(topic)
        # Last use of the bus from this thread: the unsubscribes above went out
        # through its PUSH sockets, so they are only free to close now.
        self.bus.release_thread()

    def announce_presence(self, username, online):
        self._publish(
            PRESENCE_TOPIC,
            {"type": PRESENCE, "username": username, "online": online},
            set(self._roster()),
        )

    # -- operations -----------------------------------------------------------

    def require_member(self, room_id, username):
        # Room ids arrive from a caller and reach SQLite as a bound parameter,
        # which rejects anything but a scalar. An id of the wrong type is not a
        # room that exists, so it gets the answer a missing one gets.
        if not isinstance(room_id, str):
            raise MessagingError(f"No such room: {room_id!r}")
        room = self.timeline.room(room_id)
        if room is None:
            raise MessagingError(f"No such room: {room_id}")
        if username not in room["members"]:
            raise MessagingError("Not a member of that room")
        return room

    def sync(self, username, subscriber=None):
        """Everything a freshly connected client needs, and the subscriptions."""
        rooms = self.timeline.rooms_for(username)
        for room in rooms:
            self._watch(subscriber, room["id"])

        present = set(self.timeline.online())
        return {
            "me": username,
            "users": [
                {"username": name, "online": name in present}
                for name in sorted(self._roster())
            ],
            "rooms": rooms,
        }

    def history(self, username, room_id, since=0):
        room = self.require_member(room_id, username)
        since = cursor(since)
        # `lastSeq` is what makes a truncated reply detectable: the cap is on the
        # tail, so a client further behind than the limit gets the newest slice
        # and would otherwise have no way to know it skipped the rest.
        return {
            "room": room_id,
            "since": since,
            "lastSeq": room["lastSeq"],
            "messages": self.timeline.history(room_id, since),
        }

    def send(self, username, room_id, body):
        room = self.require_member(room_id, username)
        body = str(body or "").strip()
        if not body:
            raise MessagingError("Empty message")

        message = self.timeline.append(room_id, username, body)
        self._publish(room_topic(room_id), {"type": MESSAGE, **message}, room["members"])
        return {"ok": True, "seq": message["seq"]}

    def open_room(self, username, members=(), title=None, subscriber=None):
        """Create a room from a set of people. No name required, no ceremony."""
        known = {str(name) for name in members or []} & set(self._roster())
        known.add(username)
        title = str(title or "").strip() or ", ".join(sorted(known))

        room = self.timeline.create_room(title, sorted(known))
        self._watch(subscriber, room["id"])
        self._announce_room(room)
        return room

    def invite(self, username, room_id, invited):
        """Add a person to a conversation: the membership simply grows."""
        invited = str(invited or "")
        room = self.require_member(room_id, username)
        if invited not in set(self._roster()):
            raise MessagingError(f"No such user: {invited}")

        added = self.timeline.add_members(room_id, [invited])
        if not added:
            return {"ok": True, "room": room}

        self.post_event(room_id, f"{username} added {invited}", set(room["members"]) | {invited})
        updated = self.timeline.room(room_id)
        self._announce_room(updated)
        return {"ok": True, "room": updated}

    def leave(self, username, room_id):
        room = self.require_member(room_id, username)
        self.timeline.remove_member(room_id, username)
        self.post_event(room_id, f"{username} left", set(room["members"]))
        self._announce_room(self.timeline.room(room_id))
        return {"ok": True}

    def merge(self, username, source_id, target_id):
        """Fold one conversation's membership into another's.

        History is not rewritten. Sequence numbers are per room and a client
        holds a cursor into each, so renumbering one room's messages into
        another would invalidate every cursor pointing at either. The rooms keep
        their own timelines and each gets a marker saying what happened.
        """
        source = self.require_member(source_id, username)
        target = self.require_member(target_id, username)

        added = self.timeline.add_members(target_id, source["members"])
        merged = self.timeline.room(target_id)

        self.post_event(
            target_id,
            f"{username} merged {source['title']} in"
            + (f", adding {', '.join(added)}" if added else ""),
            set(merged["members"]),
        )
        self.post_event(
            source_id,
            f"{username} merged this into {target['title']}",
            set(source["members"]),
        )
        self._announce_room(merged)
        return {"ok": True, "room": merged}

    # -- streams --------------------------------------------------------------

    def ensure_stream(self, room_id, title, members):
        """Declare a machine stream. Idempotent, so it can run on every boot."""
        return self.timeline.create_room(title, sorted(members), kind=STREAM, room_id=room_id)

    def post_event(self, room_id, text, audience):
        """Append a machine event to a room and push it."""
        message = self.timeline.append(room_id, "system", text, kind=EVENT)
        self._publish(room_topic(room_id), {"type": MESSAGE, **message}, audience)
        return message

    def post_event_quietly(self, room_id, text, audience):
        """`post_event`, but a failure to publish is swallowed.

        For events raised as a side effect of doing something else, where the
        stream is a convenience and must never be able to fail the work that
        produced it.
        """
        try:
            return self.post_event(room_id, text, audience)
        except Exception:  # pragma: no cover - a stream must never break a caller
            logger.debug("Could not publish a machine event", exc_info=True)
            return None

    def _announce_room(self, room):
        """Tell members a room's shape changed, on the presence topic.

        Presence rather than the room topic: someone just added has not
        subscribed to it yet, and this is the message that tells them to.
        """
        if room is not None:
            self._publish(PRESENCE_TOPIC, {"type": ROOM, "room": room}, room["members"])
