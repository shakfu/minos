"""WebSocket transport for the OS.js client.

The client opens one socket to `/` and multiplexes named messages over it as
JSON `{name, params}` frames. The server pushes broadcasts (hot reload, package
manifest changes, keepalive pings); the client sends application messages back.
"""

import json
import logging
import os
import threading
import time

logger = logging.getLogger(__name__)

# Inbound names are client-controlled, so only this one internal name is
# accepted. Without the guard a page could forge events such as
# osjs/core:logged-in and drive server-side handlers that trust them.
APPLICATION_MESSAGE = "osjs/application:socket:message"

# Package name -> handler(connection, respond, args). The OS.js equivalent of a
# package's server script. Empty until a package registers one.
APPLICATION_HANDLERS = {}


def register_application_handler(name, handler):
    APPLICATION_HANDLERS[name] = handler


class Connection:
    """One connected client. Sends are serialised so frames cannot interleave."""

    def __init__(self, ws, user):
        self.ws = ws
        self.user = user
        self._lock = threading.Lock()

    def send(self, name, params=None):
        frame = json.dumps({"name": name, "params": params or []})
        with self._lock:
            self.ws.send(frame)


class Registry:
    """The set of live connections, and the fan-out over them."""

    def __init__(self):
        self._connections = set()
        self._lock = threading.Lock()

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

        reached = 0
        for connection in targets:
            try:
                connection.send(name, params)
                reached += 1
            except Exception:
                # The peer can drop between the snapshot and the send; its own
                # serve() loop removes it from the registry.
                logger.debug("Dropping broadcast to a closed socket", exc_info=True)
        return reached

    def broadcast_to_user(self, username, name, params=None):
        return self.broadcast(name, params, lambda c: c.user.get("username") == username)


def dispatch(connection, raw):
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

    return _handle_application_message(connection, params)


def _handle_application_message(connection, params):
    if not params or not isinstance(params[0], dict):
        return False

    pid = params[0].get("pid")
    name = params[0].get("name")
    args = params[0].get("args") or []

    handler = APPLICATION_HANDLERS.get(name)
    if handler is None:
        logger.debug("Application %s has no server handler", name)
        return False

    def respond(*response):
        connection.send(APPLICATION_MESSAGE, [{"pid": pid, "args": list(response)}])

    handler(connection, respond, args)
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
                dispatch(connection, raw)
    finally:
        registry.remove(connection)


class DistWatcher:
    """Polls the build output and pushes reload signals to connected clients.

    Only the top level of dist/ is scanned. That covers the `npm run watch`
    loop, which is what the signal exists for; package directories below it are
    symlinks into node_modules and are not followed.
    """

    def __init__(self, registry, root, interval=1.0):
        self.registry = registry
        self.root = root
        self.interval = interval
        self._stamps = self._scan()

    def _scan(self):
        stamps = {}
        try:
            entries = list(os.scandir(self.root))
        except OSError:
            return stamps

        for entry in entries:
            if not entry.name.endswith((".js", ".css")) and entry.name != "metadata.json":
                continue
            try:
                if not entry.is_file():
                    continue
                stat = entry.stat()
            except OSError:
                continue
            stamps[entry.name] = (stat.st_mtime_ns, stat.st_size)
        return stamps

    def poll(self):
        """Broadcast a signal for each changed file. Returns the names sent."""
        current = self._scan()
        changed = [name for name, stamp in current.items() if self._stamps.get(name) != stamp]
        self._stamps = current

        for name in changed:
            if name == "metadata.json":
                self.registry.broadcast("osjs/packages:metadata:changed")
            else:
                self.registry.broadcast("osjs/dist:changed", [f"/{name}"])
        return changed

    def start(self):
        thread = threading.Thread(target=self._loop, name="dist-watcher", daemon=True)
        thread.start()
        return thread

    def _loop(self):
        while True:
            time.sleep(self.interval)
            try:
                self.poll()
            except Exception:
                logger.warning("Dist watcher poll failed", exc_info=True)
