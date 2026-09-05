"""WebSocket transport.

A client opens one socket to `/` and multiplexes named messages over it as
JSON `{name, params}` frames. The server pushes a handshake and keepalive
pings; the client sends application messages back. Nothing here assumes what
the client is: the terminal client in `tui/` speaks exactly this.
"""

import json
import logging
import threading

logger = logging.getLogger(__name__)

# Inbound names are client-controlled, so only this one internal name is
# accepted. Without the guard a page could forge events such as
# osjs/core:logged-in and drive server-side handlers that trust them.
APPLICATION_MESSAGE = "osjs/application:socket:message"


def encode(name, params=None):
    """One frame, as the wire carries it.

    Separate from `Connection.send` so a fan-out can serialise once rather than
    once per recipient: the frame is identical for everybody it reaches.
    """
    return json.dumps({"name": name, "params": params or []})


class Connection:
    """One connected client. Sends are serialised so frames cannot interleave."""

    def __init__(self, ws, user):
        self.ws = ws
        self.user = user
        self._lock = threading.Lock()

    def send(self, name, params=None):
        self.send_frame(encode(name, params))

    def send_frame(self, frame):
        """Write an already-encoded frame. The lock is what keeps two threads
        from interleaving the halves of one message on a single socket."""
        with self._lock:
            self.ws.send(frame)


class Registry:
    """The set of live connections, the fan-out over them, and the handlers.

    The handler map belongs here rather than to the module so that it is scoped
    to one application. As a global it was shared by every app in the process,
    so a second one silently took over the first one's handlers -- which the
    tests had to work around by clearing it between cases.
    """

    def __init__(self):
        self._connections = set()
        self._lock = threading.Lock()
        # Package name -> handler(connection, respond, args). The OS.js
        # equivalent of a package's server script. Empty until one registers.
        self.application_handlers = {}

    def register_application_handler(self, name, handler):
        self.application_handlers[name] = handler

    def __len__(self):
        with self._lock:
            return len(self._connections)

    def add(self, connection):
        with self._lock:
            self._connections.add(connection)

    def remove(self, connection):
        with self._lock:
            self._connections.discard(connection)

    def broadcast(self, name, params=None, predicate=None):
        """Send to every matching connection. Returns the number reached."""
        with self._lock:
            targets = [c for c in self._connections if predicate is None or predicate(c)]

        if not targets:
            return 0

        # Encoded once for the whole fan-out. Every recipient gets the same
        # bytes, and a broadcast to a busy room would otherwise repeat the same
        # `json.dumps` for each of them.
        frame = encode(name, params)

        reached = 0
        for connection in targets:
            try:
                connection.send_frame(frame)
                reached += 1
            except Exception:
                # The peer can drop between the snapshot and the send; its own
                # serve() loop removes it from the registry.
                logger.debug("Dropping broadcast to a closed socket", exc_info=True)
        return reached

    def broadcast_to_user(self, username, name, params=None):
        return self.broadcast(name, params, lambda c: c.user.get("username") == username)


def dispatch(registry, connection, raw):
    """Route one inbound frame. Malformed and forged frames are dropped."""
    try:
        message = json.loads(raw)
        name = message["name"]
        params = message.get("params") or []
    except (TypeError, ValueError, KeyError, AttributeError):
        logger.warning("Discarding malformed socket frame")
        return False

    if not isinstance(name, str) or not isinstance(params, list):
        logger.warning("Discarding malformed socket frame")
        return False

    if name.startswith("osjs") and name != APPLICATION_MESSAGE:
        logger.warning("Refusing forged internal message %s", name)
        return False

    if name != APPLICATION_MESSAGE:
        logger.debug("No handler for socket message %s", name)
        return False

    return _handle_application_message(registry, connection, params)


def _handle_application_message(registry, connection, params):
    if not params or not isinstance(params[0], dict):
        return False

    pid = params[0].get("pid")
    name = params[0].get("name")
    args = params[0].get("args") or []

    handler = registry.application_handlers.get(name)
    if handler is None:
        logger.debug("Application %s has no server handler", name)
        return False

    def respond(*response):
        connection.send(APPLICATION_MESSAGE, [{"pid": pid, "args": list(response)}])

    # A handler is application code reached by a client-supplied payload, and an
    # exception here would unwind through serve() and close the socket -- so one
    # bad field would cost a client its connection rather than earning an error
    # reply. The guard belongs at the dispatch point so it covers every handler
    # rather than the fields one of them happens to validate.
    try:
        handler(connection, respond, args)
    except Exception:
        logger.exception("Application handler %s failed", name)
        try:
            respond({"error": "Request failed"})
        except Exception:  # pragma: no cover - the peer is already gone
            logger.debug("Could not report a handler failure", exc_info=True)
    return True


def serve(registry, ws, user, ping_interval, session_max_age):
    """Run one connection until the client goes away.

    `receive` doubles as the keepalive clock: a timeout means the client has
    been quiet for a full interval, which is exactly when a ping is due.
    """
    connection = Connection(ws, user)
    registry.add(connection)
    try:
        connection.send("osjs/core:connected", [{"cookie": {"maxAge": session_max_age}}])
        while True:
            raw = ws.receive(timeout=ping_interval)
            if raw is None:
                connection.send("osjs/core:ping")
            else:
                dispatch(registry, connection, raw)
    finally:
        registry.remove(connection)
