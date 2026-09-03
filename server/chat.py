"""The Chat application handler: rooms, membership, presence and streams.

Registered under `register_application_handler`, so every request arrives as an
`osjs/application:socket:message` frame and none of it touches the frozen HTTP
contract. That is the reason this feature is shaped the way it is -- the
websocket is the one extension point the OS.js wire format leaves open.

A room here is not a channel someone joined. It is a set of people, and the
membership is what the window shows; a one-to-one becomes a group when another
name is added to it, with nothing created and nothing named. Machine streams
are the same object with a producer instead of a person, which is what lets one
window type display either.

The handler never touches the connection registry. It publishes to the bus and
returns; the relay decides who is locally connected and delivers. That keeps
delivery working when there is more than one worker, and it is why the handler
signature did not have to grow a registry argument.
"""

import logging

from . import bus as bus_module
from . import config, timeline

logger = logging.getLogger(__name__)

# Push event types, as seen by the client.
MESSAGE = "message"
ROOM = "room"
PRESENCE = "presence"


class ChatService:
    """Server-side state for one worker process.

    Owns nothing durable -- the timeline does -- beyond which topics this
    worker's connections have made it care about.
    """

    def __init__(self, bus, registry):
        self.bus = bus
        self.registry = registry
        self._subscriptions = {}

    # -- delivery -------------------------------------------------------------

    def deliver(self, topic, payload):
        """Hand a bus message to the local websockets it is addressed to.

        The publisher works out the recipients, so a worker never queries
        membership to deliver: the audience travels with the message.
        """
        audience = payload.get("to")
        event = payload.get("event") or {}

        def wanted(connection):
            return audience is None or connection.user.get("username") in audience

        self.registry.broadcast(
            "osjs/application:socket:message",
            [{"pid": None, "name": "Chat", "args": [event]}],
            predicate=wanted,
        )

    def _publish(self, topic, event, audience):
        self.bus.publish(topic, {"to": sorted(audience), "event": event})

    def _watch(self, connection, room_id):
        """Subscribe this worker to a room for as long as a client wants it.

        Keyed by the websocket rather than the `Connection`, because the socket
        route holds the former and the handler is given the latter; that is
        what lets subscriptions be released on disconnect without `sockets`
        growing a callback.
        """
        topic = bus_module.room_topic(room_id)
        watched = self._subscriptions.setdefault(id(connection.ws), set())
        if topic not in watched:
            watched.add(topic)
            self.bus.subscribe(topic)

    def connect(self, ws):
        self._subscriptions.setdefault(id(ws), set()).add(bus_module.PRESENCE_TOPIC)
        self.bus.subscribe(bus_module.PRESENCE_TOPIC)

    def disconnect(self, ws):
        for topic in self._subscriptions.pop(id(ws), ()):
            self.bus.unsubscribe(topic)

    def announce_presence(self, username, online):
        self._publish(
            bus_module.PRESENCE_TOPIC,
            {"type": PRESENCE, "username": username, "online": online},
            set(config.USERS),
        )

    # -- operations -----------------------------------------------------------

    def handle(self, connection, respond, args):
        request = args[0] if args and isinstance(args[0], dict) else {}
        operation = request.get("op")
        username = connection.user.get("username")

        handlers = {
            "sync": self._sync,
            "history": self._history,
            "send": self._send,
            "open": self._open,
            "invite": self._invite,
            "leave": self._leave,
            "merge": self._merge,
        }
        handler = handlers.get(operation)
        if handler is None:
            respond({"error": f"No such chat operation: {operation}"})
            return

        try:
            respond(handler(connection, username, request))
        except PermissionError as error:
            respond({"error": str(error)})

    def _require_member(self, room_id, username):
        room = timeline.room(room_id)
        if room is None:
            raise PermissionError(f"No such room: {room_id}")
        if username not in room["members"]:
            raise PermissionError("Not a member of that room")
        return room

    def _sync(self, connection, username, request):
        """Everything a freshly opened desktop needs, and the subscriptions."""
        rooms = timeline.rooms_for(username)
        for room in rooms:
            self._watch(connection, room["id"])

        present = set(timeline.online())
        return {
            "me": username,
            "users": [
                {"username": name, "online": name in present} for name in sorted(config.USERS)
            ],
            "rooms": rooms,
        }

    def _history(self, connection, username, request):
        room_id = request.get("room")
        self._require_member(room_id, username)
        since = int(request.get("since") or 0)
        return {"room": room_id, "since": since, "messages": timeline.history(room_id, since)}

    def _send(self, connection, username, request):
        room_id = request.get("room")
        body = str(request.get("body") or "").strip()
        room = self._require_member(room_id, username)
        if not body:
            return {"error": "Empty message"}

        message = timeline.append(room_id, username, body)
        self._publish(
            bus_module.room_topic(room_id), {"type": MESSAGE, **message}, room["members"]
        )
        return {"ok": True, "seq": message["seq"]}

    def _open(self, connection, username, request):
        """Create a room from a set of people. No name required, no ceremony."""
        members = {str(name) for name in request.get("members") or []} & set(config.USERS)
        members.add(username)
        title = str(request.get("title") or "").strip() or ", ".join(sorted(members))

        room = timeline.create_room(title, sorted(members))
        self._watch(connection, room["id"])
        self._announce_room(room)
        return room

    def _invite(self, connection, username, request):
        """Drag a person onto a window: the conversation simply grows."""
        room_id = request.get("room")
        invited = str(request.get("username") or "")
        room = self._require_member(room_id, username)
        if invited not in config.USERS:
            return {"error": f"No such user: {invited}"}

        added = timeline.add_members(room_id, [invited])
        if not added:
            return {"ok": True, "room": room}

        self.post_event(room_id, f"{username} added {invited}", set(room["members"]) | {invited})
        updated = timeline.room(room_id)
        self._announce_room(updated)
        return {"ok": True, "room": updated}

    def _leave(self, connection, username, request):
        room_id = request.get("room")
        room = self._require_member(room_id, username)
        timeline.remove_member(room_id, username)
        self.post_event(room_id, f"{username} left", set(room["members"]))
        self._announce_room(timeline.room(room_id))
        return {"ok": True}

    def _merge(self, connection, username, request):
        """Drop one window onto another: the two memberships become one.

        History is not rewritten. Sequence numbers are per room and a client
        holds a cursor into each, so renumbering one room's messages into
        another would invalidate every cursor pointing at either. The rooms
        keep their own timelines and each gets a marker saying what happened.
        """
        source_id = request.get("room")
        target_id = request.get("into")
        source = self._require_member(source_id, username)
        target = self._require_member(target_id, username)

        added = timeline.add_members(target_id, source["members"])
        merged = timeline.room(target_id)

        self.post_event(
            target_id,
            f"{username} merged {source['title']} in" + (f", adding {', '.join(added)}" if added else ""),
            set(merged["members"]),
        )
        self.post_event(source_id, f"{username} merged this into {target['title']}", set(source["members"]))
        self._announce_room(merged)
        return {"ok": True, "room": merged}

    # -- publishing helpers ---------------------------------------------------

    def post_event(self, room_id, text, audience):
        message = timeline.append(room_id, "system", text, kind=timeline.EVENT)
        self._publish(bus_module.room_topic(room_id), {"type": MESSAGE, **message}, audience)
        return message

    def _announce_room(self, room):
        """Tell members a room's shape changed, on the presence topic.

        Presence rather than the room topic: someone just added has not
        subscribed to it yet, and this is the message that tells them to.
        """
        if room is not None:
            self._publish(bus_module.PRESENCE_TOPIC, {"type": ROOM, "room": room}, room["members"])


def ensure_system_stream():
    """The one room nobody creates: machine events, everyone a member."""
    return timeline.create_room(
        "System", sorted(config.USERS), kind=timeline.STREAM, room_id=config.SYSTEM_STREAM
    )


def publish_system_event(service, text):
    """Append to the system stream and push it.

    This is the machine half of the design: a stream produced by the server
    rather than typed by anyone, arriving in the same window type as a chat.
    """
    try:
        service.post_event(config.SYSTEM_STREAM, text, set(config.USERS))
    except Exception:  # pragma: no cover - a stream must never break a request
        logger.debug("Could not publish a system event", exc_info=True)
