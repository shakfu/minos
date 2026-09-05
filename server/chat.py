"""Binds the messaging layer to this server's websocket protocol.

Everything conversational lives in `messaging/`, which knows nothing about
Flask, the OS.js wire format, or who has an account here. This module is the
seam: it maps operation names to messaging calls, turns a `MessagingError` into
the error shape the client expects, and fans deliveries out over the connection
registry as `osjs/application:socket:message` frames.

The chat feature is shaped around the websocket because that is the one
extension point the frozen OS.js HTTP contract leaves open -- so none of this
touches the route map.
"""

import logging

from messaging import Messaging, MessagingError, Timeline

from . import config

logger = logging.getLogger(__name__)

APPLICATION = "Chat"
APPLICATION_MESSAGE = "osjs/application:socket:message"


def build(bus, registry):
    """Assemble the messaging layer against this server's transport."""
    timeline = Timeline(
        db_path=config.TIMELINE_DB,
        run_dir=config.RUN_DIR,
        history_limit=config.HISTORY_LIMIT,
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

    # The socket route drives these, and neither it nor `sockets` needs to know
    # what a subscription is. A connection's websocket identifies the
    # subscriber, because that is the object the route holds.
    def connect(self, ws):
        self.service.connect(id(ws))

    def disconnect(self, ws):
        self.service.disconnect(id(ws))

    def announce_presence(self, username, online):
        self.service.announce_presence(username, online)

    def ensure_system_stream(self):
        """The one room nobody creates: machine events, everyone a member."""
        return self.service.ensure_stream(
            config.SYSTEM_STREAM, "System", sorted(config.USERS)
        )

    def publish_system_event(self, text):
        """Announce a server-side event on the system stream.

        The machine half of the design: a stream produced by the server rather
        than typed by anyone, arriving in the same window type as a chat. A
        failure to publish is swallowed -- a stream must never be able to fail a
        request that has already been carried out.
        """
        self.service.post_event_quietly(config.SYSTEM_STREAM, text, set(config.USERS))

    def handle(self, connection, respond, args):
        """Route one `osjs/application:socket:message` frame."""
        request = args[0] if args and isinstance(args[0], dict) else {}
        operation = request.get("op")
        username = connection.user.get("username")
        subscriber = id(connection.ws)

        operations = {
            "sync": lambda: self.service.sync(username, subscriber),
            "history": lambda: self.service.history(
                username, request.get("room"), request.get("since")
            ),
            "send": lambda: self.service.send(
                username, request.get("room"), request.get("body")
            ),
            "open": lambda: self.service.open_room(
                username, request.get("members"), request.get("title"), subscriber
            ),
            "invite": lambda: self.service.invite(
                username, request.get("room"), request.get("username")
            ),
            "leave": lambda: self.service.leave(username, request.get("room")),
            "merge": lambda: self.service.merge(
                username, request.get("room"), request.get("into")
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
