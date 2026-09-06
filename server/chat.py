"""Binds the messaging layer to this server's websocket protocol.

Everything conversational lives in `messaging/`, which knows nothing about
Flask, the OS.js wire format, or who has an account here. This module is the
seam: it maps operation names to messaging calls, turns a `MessagingError` into
the error shape the client expects, and fans deliveries out over the connection
registry as `osjs/application:socket:message` frames.

Two things this layer owns rather than `messaging/`:

- **Who is an administrator.** The messaging layer takes it as an argument, so
  the question of who counts stays with the host that has the accounts. Here it
  is a name in `config.ADMINS`, carried on the session profile's `groups`.

- **Occupancy for the life of a connection.** A user who is *in* a room holds an
  occupancy row, and a transient room dies once its last one is released. A
  connection that drops must therefore release everything it held, or a room
  nobody is in stays alive forever. The rows are keyed by websocket for exactly
  that reason.

The chat feature is shaped around the websocket because that is the one
extension point the frozen OS.js HTTP contract leaves open -- so none of this
touches the route map.
"""

import logging
import threading

from messaging import Messaging, MessagingError, Timeline

from . import config

logger = logging.getLogger(__name__)

APPLICATION = "Chat"
APPLICATION_MESSAGE = "osjs/application:socket:message"

# The deletion of a transient room is a promise to the people who spoke in one,
# so the check has to run whether or not anybody is connected -- which is why it
# is a thread here rather than something a request happens to trigger.
SWEEP_INTERVAL = config.ROOM_SWEEP


def is_admin(user):
    """Administrators are named on the profile, which the session carries."""
    return "admin" in (user.get("groups") or [])


def build(bus, registry):
    """Assemble the messaging layer against this server's transport."""
    timeline = Timeline(
        db_path=config.TIMELINE_DB,
        run_dir=config.RUN_DIR,
        history_limit=config.HISTORY_LIMIT,
        grace=config.ROOM_GRACE,
    ).init()

    def deliver(audience, event):
        """Fan one bus message out to the local sockets it is addressed to."""

        def wanted(connection):
            return audience is None or connection.user.get("username") in audience

        registry.broadcast(
            APPLICATION_MESSAGE,
            [{"pid": None, "name": APPLICATION, "args": [event]}],
            predicate=wanted,
        )

    service = Messaging(timeline, bus, deliver=deliver, roster=lambda: set(config.USERS))
    return ChatHandler(service)


class ChatHandler:
    """The websocket-facing half: operation names in, reply dictionaries out."""

    def __init__(self, service):
        self.service = service
        self.timeline = service.timeline
        self.worker = None
        # Websocket -> the occupancy rows it holds. A connection can be in more
        # than one room at once, and all of them are released together when it
        # goes away.
        self._occupancies = {}
        self._lock = threading.Lock()
        self._sweeper = None
        self._stopping = threading.Event()

    # The socket route drives these, and neither it nor `sockets` needs to know
    # what a subscription is. A connection's websocket identifies the
    # subscriber, because that is the object the route holds.
    def connect(self, ws):
        self.service.connect(id(ws))

    def disconnect(self, ws):
        with self._lock:
            held = self._occupancies.pop(id(ws), set())
        for occupancy in held:
            try:
                self.service.exit(occupancy)
            except Exception:  # pragma: no cover - teardown must not raise
                logger.debug("Could not release occupancy %s", occupancy, exc_info=True)
        self.service.disconnect(id(ws))

    def announce_presence(self, username, online):
        self.service.announce_presence(username, online)

    # -- the sweep ------------------------------------------------------------

    def start_sweeper(self):
        """Run the transient-room sweep until the process stops."""

        def run():
            while not self._stopping.wait(SWEEP_INTERVAL):
                try:
                    self.service.sweep()
                except Exception:  # pragma: no cover - a sweep must not die
                    logger.debug("Transient room sweep failed", exc_info=True)

        self._sweeper = threading.Thread(target=run, name="room-sweep", daemon=True)
        self._sweeper.start()
        return self._sweeper

    def stop_sweeper(self):
        self._stopping.set()
        if self._sweeper is not None:
            self._sweeper.join(timeout=2)
            self._sweeper = None

    # -- the system channel ---------------------------------------------------

    def ensure_system_channel(self):
        """The one space nobody is invited to: machine events, everyone subscribed.

        A channel rather than a room, because nothing typed goes into it: the
        server is its only producer and its audience may only read.
        """
        return self.service.ensure_channel(
            config.SYSTEM_CHANNEL, "System", sorted(config.USERS)
        )

    def publish_system_event(self, text):
        """Announce a server-side event on the system channel.

        A failure to publish is swallowed -- a channel must never be able to fail
        a request that has already been carried out.
        """
        self.service.post_event_quietly(
            config.SYSTEM_CHANNEL, text, set(config.USERS)
        )

    def _create_channel(self, username, admin, title, groups):
        """Found a channel, and say so where everybody is listening.

        A new channel has no subscribers, so the only way anyone learns it
        exists is the machine channel every account is already in.
        """
        channel = self.service.create_channel(username, admin, title, groups or ())
        self.publish_system_event(
            f"{username} opened the channel {channel['title']} ({channel['id']})"
        )
        return channel

    # -- dispatch -------------------------------------------------------------

    def handle(self, connection, respond, args):
        """Route one `osjs/application:socket:message` frame."""
        request = args[0] if args and isinstance(args[0], dict) else {}
        operation = request.get("op")
        user = connection.user
        username = user.get("username")
        admin = is_admin(user)
        subscriber = id(connection.ws)
        service = self.service

        operations = {
            "sync": lambda: service.sync(username, admin, subscriber),
            "history": lambda: service.history(
                username, request.get("room"), request.get("since")
            ),
            "send": lambda: service.send(
                username, request.get("room"), request.get("body")
            ),
            "open": lambda: service.open_room(
                username,
                request.get("invite"),
                request.get("title"),
                request.get("retention") or "persisted",
                subscriber,
            ),
            "create": lambda: service.create_room(
                username, admin, request.get("title"), request.get("invite"), subscriber
            ),
            "invite": lambda: service.invite(
                username, admin, request.get("room"), request.get("principal")
            ),
            "uninvite": lambda: service.uninvite(
                username, admin, request.get("room"), request.get("principal")
            ),
            "leave": lambda: service.leave(username, request.get("room")),
            "enter": lambda: self._enter(connection, username, request.get("room")),
            "exit": lambda: self._exit(connection, request.get("occupancy")),
            "read": lambda: service.mark_read(
                username, request.get("room"), request.get("seq")
            ),
            "group.create": lambda: service.create_group(
                admin, request.get("name"), request.get("members")
            ),
            "group.assign": lambda: service.assign_group(
                admin, request.get("group"), request.get("username")
            ),
            "group.unassign": lambda: service.unassign_group(
                admin, request.get("group"), request.get("username")
            ),
            "subscribe": lambda: service.subscribe(
                username, request.get("channel"), subscriber
            ),
            "unsubscribe": lambda: service.unsubscribe(username, request.get("channel")),
            "channel.create": lambda: self._create_channel(
                username, admin, request.get("title"), request.get("groups")
            ),
            "channel.publish": lambda: service.publish_message(
                username, admin, request.get("channel"), request.get("body")
            ),
            "channel.admit": lambda: service.admit(
                admin, request.get("channel"), request.get("group")
            ),
            "channel.revoke": lambda: service.revoke(
                admin, request.get("channel"), request.get("group")
            ),
        }

        call = operations.get(operation)
        if call is None:
            respond({"error": f"No such chat operation: {operation}"})
            return

        try:
            respond(call())
        except MessagingError as error:
            respond({"error": str(error)})

    def _enter(self, connection, username, room_id):
        """Take a place in a room, remembering it against this connection."""
        reply = self.service.enter(username, room_id, self.worker, id(connection.ws))
        with self._lock:
            self._occupancies.setdefault(id(connection.ws), set()).add(reply["occupancy"])
        return reply

    def _exit(self, connection, occupancy):
        with self._lock:
            held = self._occupancies.get(id(connection.ws), set())
            if occupancy not in held:
                # Not this connection's to release. Releasing another's would
                # let one client end a room somebody else is sitting in.
                raise MessagingError("Not in that room")
            held.discard(occupancy)
        return self.service.exit(occupancy)
