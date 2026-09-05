"""The messaging operations: groups, rooms, channels, occupancy and presence.

A room here is a *place*, not a set of people. Its identity is its own; adding
or removing someone leaves the same room, and two rooms may hold the same
people. Who may enter is a set of grants naming principals -- a user, or a whole
group -- so admission is administrable rather than personal, and a grant to a
group follows that group as it changes.

Two axes describe every room, and nothing else about one varies:

- **authority**, who founded it and therefore who may invite to it. An
  admin-founded room is populated only by admins; a user-founded one by any of
  its participants.
- **retention**, whether it is kept. A persisted room lasts until deleted. A
  transient room is deleted once its last occupant has been gone for the grace
  period, and retains nothing.

That second axis is why occupancy exists as a separate idea from access. Who
*may* be in a room and who *is* are different facts, and only the second can end
-- people do not resign from a conversation, they stop being in it.

A channel is the same storage with a different door: subscribers choose to
subscribe, and may not write. Curation of what others submit is deliberately not
here; a channel in this layer is a broadcast, which is what the system stream
has always been.

Nothing in this module knows how its callers are connected. Operations return
plain dictionaries and refusals are exceptions; deliveries go out through a
`deliver(audience, event)` callback the host provides. Whether a caller is an
administrator is likewise the host's business, passed in rather than looked up,
which is what keeps this layer independent of who has an account anywhere.

Delivery never consults the connection list. An operation appends to the
timeline, publishes to the bus, and returns; the relay in each process decides
who is locally connected and hands them over. That is what makes a second worker
process work at all.
"""

import logging

from .bus import PRESENCE_TOPIC, room_topic
from .timeline import (
    ADMIN,
    CHANNEL,
    EVENT,
    PERSISTED,
    PRINCIPAL_GROUP,
    PRINCIPAL_USER,
    ROOM,
    TRANSIENT,
    USER,
)

logger = logging.getLogger(__name__)

# Push event types, as seen by a subscriber.
MESSAGE = "message"
ROOM_EVENT = "room"
ROOM_GONE = "roomGone"
PRESENCE = "presence"
GROUP_EVENT = "group"


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
        """Hand a bus message to the host. Wire this to `Bus.start`."""
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

    # -- access ---------------------------------------------------------------

    def require_room(self, room_id, kind=None):
        # Room ids arrive from a caller and reach SQLite as a bound parameter,
        # which rejects anything but a scalar. An id of the wrong type is not a
        # room that exists, so it gets the answer a missing one gets.
        if not isinstance(room_id, str):
            raise MessagingError(f"No such room: {room_id!r}")
        room = self.timeline.room(room_id)
        if room is None or (kind is not None and room["kind"] != kind):
            raise MessagingError(f"No such room: {room_id}")
        return room

    def require_access(self, room_id, username, kind=None):
        """The room, if this user may see it. Grants and groups both count."""
        room = self.require_room(room_id, kind)
        if not self.timeline.has_access(room_id, username):
            raise MessagingError("Not invited to that room")
        return room

    def require_invite_authority(self, room, username, is_admin):
        """Who may bring someone into this room.

        Admin-founded rooms are institutional: their membership is an
        administrative fact, so a participant cannot change it. User-founded
        rooms are permissive, and deliberately so -- restricting invitation to
        the creator would buy nothing, since any participant could raise a new
        room with the same people in it.
        """
        if room["authority"] == ADMIN:
            if not is_admin:
                raise MessagingError("Only an administrator may invite to this room")
        elif not self.timeline.has_access(room["id"], username):
            raise MessagingError("Not invited to that room")

    def require_admin(self, is_admin):
        if not is_admin:
            raise MessagingError("Only an administrator may do that")

    # -- sync -----------------------------------------------------------------

    def sync(self, username, is_admin=False, subscriber=None):
        """Everything a freshly connected client needs, and the subscriptions."""
        rooms = self.timeline.rooms_for(username)
        channels = self.timeline.channels_for(username)
        for space in rooms + channels:
            self._watch(subscriber, space["id"])

        present = set(self.timeline.online())
        return {
            "me": username,
            "isAdmin": bool(is_admin),
            "users": [
                {"username": name, "online": name in present}
                for name in sorted(self._roster())
            ],
            "groups": self.timeline.groups(),
            "rooms": rooms,
            "channels": channels,
            "read": self.timeline.read_cursors(username),
        }

    def history(self, username, room_id, since=0):
        room = self.require_access(room_id, username)
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
        room = self.require_access(room_id, username)
        if room["kind"] == CHANNEL:
            # A channel is read-only to its audience. Submission for approval is
            # a separate feature and deliberately not implemented here, so there
            # is nothing for this to fall back to.
            raise MessagingError("A channel is read-only")

        body = str(body or "").strip()
        if not body:
            raise MessagingError("Empty message")

        message = self.timeline.append(room_id, username, body)
        self._publish(room_topic(room_id), {"type": MESSAGE, **message}, room["audience"])
        return {"ok": True, "seq": message["seq"]}

    # -- rooms ----------------------------------------------------------------

    def open_room(
        self,
        username,
        invitees=(),
        title=None,
        retention=PERSISTED,
        subscriber=None,
    ):
        """Raise an ad-hoc room. Any user may; the creator is a participant.

        `invitees` are `(kind, id)` pairs naming users or groups. Retention is
        chosen here and never again: a transient room cannot later be kept,
        because it retains nothing to keep.
        """
        if retention not in (PERSISTED, TRANSIENT):
            raise MessagingError(f"No such retention: {retention}")

        grants = {(PRINCIPAL_USER, username)}
        for principal in invitees or []:
            grants.add(self._principal(principal))

        title = str(title or "").strip() or self._derive_title(grants)
        room = self.timeline.create_room(
            title,
            created_by=username,
            kind=ROOM,
            authority=USER,
            retention=retention,
            grants=sorted(grants),
        )
        self._watch(subscriber, room["id"])
        self._announce_room(room)
        return room

    def create_room(self, username, is_admin, title, invitees=(), subscriber=None):
        """Found a permanent room. Admins only, and always persisted."""
        self.require_admin(is_admin)
        title = str(title or "").strip()
        if not title:
            raise MessagingError("A permanent room needs a name")

        # A permanent room's title is a *name*: institutional, chosen, and
        # meant to be referred to. "Post it in Engineering" only works if that
        # resolves to one room, so the name is unique among permanent rooms --
        # and the duplicate this refuses is nearly always an accident.
        #
        # Ad-hoc rooms are deliberately exempt. Their title describes who is in
        # them rather than naming them, and two conversations between the same
        # people are two conversations; requiring those to differ would be
        # membership-as-identity coming back in through the door.
        existing = self.timeline.room_named(title)
        if existing is not None:
            raise MessagingError(f"A permanent room called {title!r} already exists")

        grants = {(PRINCIPAL_USER, username)}
        for principal in invitees or []:
            grants.add(self._principal(principal))

        room = self.timeline.create_room(
            title,
            created_by=username,
            kind=ROOM,
            authority=ADMIN,
            retention=PERSISTED,
            grants=sorted(grants),
        )
        self._watch(subscriber, room["id"])
        self._announce_room(room)
        return room

    def _principal(self, principal):
        """Normalise one invitee into a `(kind, id)` pair that exists."""
        if isinstance(principal, str):
            principal = {"kind": PRINCIPAL_USER, "id": principal}
        if not isinstance(principal, dict):
            raise MessagingError(f"Not a principal: {principal!r}")

        kind = principal.get("kind", PRINCIPAL_USER)
        identifier = principal.get("id")
        if not isinstance(identifier, str):
            raise MessagingError(f"Not a principal: {principal!r}")

        if kind == PRINCIPAL_USER:
            if identifier not in set(self._roster()):
                raise MessagingError(f"No such user: {identifier}")
        elif kind == PRINCIPAL_GROUP:
            if self.timeline.group(identifier) is None:
                raise MessagingError(f"No such group: {identifier}")
        else:
            raise MessagingError(f"No such principal kind: {kind}")
        return (kind, identifier)

    def _derive_title(self, grants):
        """A name for a room nobody named: who is in it."""
        names = []
        for kind, identifier in sorted(grants):
            if kind == PRINCIPAL_GROUP:
                group = self.timeline.group(identifier)
                names.append(group["name"] if group else identifier)
            else:
                names.append(identifier)
        return ", ".join(names) or "Room"

    def invite(self, username, is_admin, room_id, principal):
        """Admit a principal. The room's authority decides who may."""
        room = self.require_room(room_id, ROOM)
        self.require_invite_authority(room, username, is_admin)
        kind, identifier = self._principal(principal)

        if not self.timeline.add_grant(room_id, kind, identifier):
            return {"ok": True, "room": room}

        label = identifier
        if kind == PRINCIPAL_GROUP:
            group = self.timeline.group(identifier)
            label = f"group {group['name']}" if group else identifier

        updated = self.timeline.room(room_id)
        self.post_event(room_id, f"{username} invited {label}", set(updated["audience"]))
        updated = self.timeline.room(room_id)
        self._announce_room(updated)
        return {"ok": True, "room": updated}

    def uninvite(self, username, is_admin, room_id, principal):
        """Withdraw a grant. Same authority as issuing one."""
        room = self.require_room(room_id, ROOM)
        self.require_invite_authority(room, username, is_admin)
        kind, identifier = self._principal(principal)

        audience = set(room["audience"])
        if not self.timeline.remove_grant(room_id, kind, identifier):
            return {"ok": True, "room": room}

        updated = self.timeline.room(room_id)
        self.post_event(room_id, f"{username} removed {identifier}", audience)
        self._announce_room(updated, audience=audience)
        return {"ok": True, "room": updated}

    def leave(self, username, room_id):
        """Give up one's own place in a room.

        Only a grant naming the user directly can be given up. Access inherited
        from a group is not this user's to drop -- it would be restored the
        moment the grant was re-evaluated -- so leaving such a room is refused
        rather than silently undone later.
        """
        room = self.require_access(room_id, username, ROOM)
        audience = set(room["audience"])

        if not self.timeline.remove_grant(room_id, PRINCIPAL_USER, username):
            raise MessagingError("Access to this room comes from a group, so it cannot be left")

        self.post_event(room_id, f"{username} left", audience)
        self._announce_room(self.timeline.room(room_id), audience=audience)
        return {"ok": True}

    # -- occupancy ------------------------------------------------------------

    def enter(self, username, room_id, worker, subscriber=None):
        """Take a place in a room. Distinct from being invited to it.

        A transient room's life is measured from the moment its last occupant
        leaves, so this is what keeps one alive -- and what rescues one during
        its grace period.
        """
        room = self.require_access(room_id, username, ROOM)
        self._watch(subscriber, room_id)
        occupancy = self.timeline.enter(room_id, username, worker)
        self._announce_room(self.timeline.room(room_id))
        return {"ok": True, "occupancy": occupancy, "room": room["id"]}

    def exit(self, occupancy_id):
        """Give up a place. Starts the countdown if it was the last one."""
        room_id = self.timeline.exit(occupancy_id)
        if room_id is not None:
            room = self.timeline.room(room_id)
            if room is not None:
                self._announce_room(room)
        return {"ok": True}

    def sweep(self):
        """Delete transient rooms whose grace period has run out.

        The audience is read before the room goes, because afterwards there is
        nobody to tell: a client that is not told would keep the room in its
        list forever.
        """
        gone = []
        for room_id in self.timeline.expired_transient_rooms():
            room = self.timeline.room(room_id)
            audience = set(room["audience"]) if room else set()
            self.timeline.delete_room(room_id)
            self._publish(
                PRESENCE_TOPIC, {"type": ROOM_GONE, "room": room_id}, audience
            )
            gone.append(room_id)
        return gone

    # -- read state -----------------------------------------------------------

    def mark_read(self, username, room_id, seq):
        self.require_access(room_id, username)
        self.timeline.mark_read(room_id, username, cursor(seq))
        return {"ok": True, "room": room_id, "seq": cursor(seq)}

    # -- groups ---------------------------------------------------------------

    def create_group(self, is_admin, name, members=()):
        self.require_admin(is_admin)
        name = str(name or "").strip()
        if not name:
            raise MessagingError("A group needs a name")

        known = {str(member) for member in members or []} & set(self._roster())
        group = self.timeline.create_group(name, members=sorted(known))
        self._announce_group(group)
        return group

    def assign_group(self, is_admin, group_id, username):
        """Add a user to a group, and with it every room the group was invited to."""
        self.require_admin(is_admin)
        if self.timeline.group(group_id) is None:
            raise MessagingError(f"No such group: {group_id}")
        if username not in set(self._roster()):
            raise MessagingError(f"No such user: {username}")

        self.timeline.assign_group(group_id, username)
        group = self.timeline.group(group_id)
        self._announce_group(group)
        # The new member's room list just changed, and nothing else would tell
        # them: they were not in the audience of any of those rooms a moment ago.
        for room in self.timeline.rooms_for(username):
            self._announce_room(room)
        return group

    def unassign_group(self, is_admin, group_id, username):
        self.require_admin(is_admin)
        if self.timeline.group(group_id) is None:
            raise MessagingError(f"No such group: {group_id}")

        # Read before the change: afterwards these rooms are no longer theirs,
        # so this is the last moment their client can be told to drop them.
        losing = [room["id"] for room in self.timeline.rooms_for(username)]
        self.timeline.unassign_group(group_id, username)
        keeping = {room["id"] for room in self.timeline.rooms_for(username)}

        group = self.timeline.group(group_id)
        self._announce_group(group)
        for room_id in losing:
            if room_id not in keeping:
                self._publish(
                    PRESENCE_TOPIC, {"type": ROOM_GONE, "room": room_id}, {username}
                )
        return group

    # -- channels -------------------------------------------------------------

    def ensure_channel(self, channel_id, title, subscribers=()):
        """Declare a channel. Idempotent, so it can run on every boot."""
        channel = self.timeline.create_room(
            title,
            created_by="system",
            kind=CHANNEL,
            authority=ADMIN,
            retention=PERSISTED,
            room_id=channel_id,
        )
        for username in subscribers:
            self.timeline.subscribe(channel_id, username)
        return self.timeline.room(channel_id)

    def subscribe(self, username, channel_id):
        """Join a channel's audience. The one thing a user chooses for themselves."""
        self.require_room(channel_id, CHANNEL)
        self.timeline.subscribe(channel_id, username)
        channel = self.timeline.room(channel_id)
        self._announce_room(channel)
        return channel

    def unsubscribe(self, username, channel_id):
        self.require_room(channel_id, CHANNEL)
        self.timeline.unsubscribe(channel_id, username)
        self._publish(PRESENCE_TOPIC, {"type": ROOM_GONE, "room": channel_id}, {username})
        return {"ok": True}

    def post_event(self, room_id, text, audience):
        """Append a machine event to a room or channel and push it."""
        message = self.timeline.append(room_id, "system", text, kind=EVENT)
        self._publish(room_topic(room_id), {"type": MESSAGE, **message}, audience)
        return message

    def publish(self, channel_id, text):
        """Put a message on a channel. The producer's path, not a subscriber's."""
        channel = self.require_room(channel_id, CHANNEL)
        return self.post_event(channel_id, text, set(channel["audience"]))

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

    # -- announcements --------------------------------------------------------

    def _announce_room(self, room, audience=None):
        """Tell a room's audience that its shape changed, on the presence topic.

        Presence rather than the room topic: someone just invited has not
        subscribed to it yet, and this is the message that tells them to.
        """
        if room is not None:
            self._publish(
                PRESENCE_TOPIC,
                {"type": ROOM_EVENT, "room": room},
                set(room["audience"]) | set(audience or ()),
            )

    def _announce_group(self, group):
        if group is not None:
            self._publish(
                PRESENCE_TOPIC, {"type": GROUP_EVENT, "group": group}, set(self._roster())
            )
