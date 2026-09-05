"""The client half of the chat protocol.

Everything rides `osjs/application:socket:message`, which is the only inbound
name the server accepts. A request carries a `pid` the server quotes back, so
several in flight at once can be told apart; a frame whose `pid` is null is not
an answer to anything but a push from the bus.

The bus does not guarantee delivery -- it is ZeroMQ PUB/SUB, which drops rather
than queues -- so every arriving message is treated as possibly out of order,
duplicated or missing. The per-room sequence number is what makes that
recoverable: a gap between the cursor and what just arrived is repaired by
asking for the difference. One mechanism covers a dropped frame, a subscription
that had not propagated, and a reconnect after an hour offline.

Three threads meet here, and keeping them apart is what the queue is for:

- the **reader**, owned by the socket, which only ever resolves a pending reply
  or hands a push to the queue. It must never block, because every reply in the
  process arrives through it.
- the **applier**, which drains that queue. Repairing a gap means *making a
  request*, and a request waits for the reader -- so doing it on the reader
  would deadlock the client against itself.
- the **caller**, usually a user interface, which makes requests and reads
  state under the same lock the applier writes it with.
"""

import queue
import threading
import time

from .transport import APPLICATION_MESSAGE, TransportError

APPLICATION = "Chat"

# How long to wait for a reply before giving up on it. Local and quick in
# practice; the ceiling exists so a lost frame surfaces as an error rather than
# a hang.
REQUEST_TIMEOUT = 10.0

# Messages kept per room. A terminal shows a screenful; this is scrollback.
LOG_LIMIT = 1000


class ChatError(Exception):
    """A refusal from the server, or a request that never came back."""


class ChatClient:
    """Protocol state: who exists, what rooms there are, and what was said."""

    def __init__(self, socket):
        self.socket = socket
        socket.on_frame = self._on_frame
        socket.on_close = self._fail_pending

        self.me = ""
        self.is_admin = False
        self.users = []
        self.groups = []
        self.rooms = {}
        self.channels = {}
        self.log = {}
        self.read = {}
        self.connected = False

        # Highest sequence applied per room: the cursor a gap is measured
        # against, and a different fact from `read`.
        self._cursors = {}
        self._repairing = set()

        self._pid = 0
        self._pending = {}
        self._lock = threading.RLock()
        self._pushes = queue.Queue()
        self._stopping = threading.Event()
        self._applier = threading.Thread(
            target=self._apply_pushes, name="chat-applier", daemon=True
        )
        self._applier.start()

        # Called with no arguments whenever anything a view would draw changed.
        self.on_change = lambda: None
        self.on_notice = lambda text: None

    # -- requests -------------------------------------------------------------

    def request(self, op, **body):
        """One round trip. Raises ChatError for a refusal or a timeout."""
        with self._lock:
            self._pid += 1
            pid = self._pid
            slot = {"event": threading.Event(), "reply": None}
            self._pending[pid] = slot

        try:
            self.socket.send(
                APPLICATION_MESSAGE,
                {"pid": pid, "name": APPLICATION, "args": [{"op": op, **body}]},
            )
        except TransportError as error:
            with self._lock:
                self._pending.pop(pid, None)
            raise ChatError(str(error)) from error

        if not slot["event"].wait(REQUEST_TIMEOUT):
            with self._lock:
                self._pending.pop(pid, None)
            raise ChatError(f"No reply to {op}")

        reply = slot["reply"]
        if isinstance(reply, dict) and "error" in reply:
            raise ChatError(reply["error"])
        return reply

    # -- frames ---------------------------------------------------------------

    def _on_frame(self, frame):
        """Called on the reader thread. Fast paths only: never block here."""
        name = frame.get("name")
        if name != APPLICATION_MESSAGE:
            return

        params = frame.get("params") or []
        envelope = params[0] if params and isinstance(params[0], dict) else None
        if envelope is None:
            return

        if envelope.get("pid") is None:
            args = envelope.get("args") or []
            if args:
                self._pushes.put(args[0])
            return

        with self._lock:
            slot = self._pending.pop(envelope["pid"], None)
        if slot is not None:
            args = envelope.get("args") or []
            slot["reply"] = args[0] if args else None
            slot["event"].set()

    def _fail_pending(self):
        self.connected = False
        with self._lock:
            pending = list(self._pending.values())
            self._pending.clear()
        for slot in pending:
            slot["reply"] = {"error": "Disconnected"}
            slot["event"].set()
        self.on_change()

    def _apply_pushes(self):
        while not self._stopping.is_set():
            try:
                event = self._pushes.get(timeout=0.2)
            except queue.Empty:
                continue
            try:
                self._dispatch(event)
            except Exception:
                # A push that cannot be applied must not end the thread; the
                # next message through re-detects any gap it left behind.
                pass

    def _dispatch(self, event):
        if not isinstance(event, dict):
            return
        kind = event.get("type")

        if kind == "message":
            self._apply(event)
        elif kind == "room":
            self._track(event.get("room") or {})
            self.on_change()
        elif kind == "roomGone":
            self._forget(event.get("room"))
            self.on_change()
        elif kind == "presence":
            for user in self.users:
                if user["username"] == event.get("username"):
                    user["online"] = bool(event.get("online"))
            self.on_change()
        elif kind == "group":
            self._track_group(event.get("group") or {})
            self.on_change()

    # -- the sequence contract ------------------------------------------------

    def _apply(self, message):
        """Apply one message against its room's cursor.

        Three cases, and the middle one is the whole point: a sequence at or
        below the cursor has been seen already, one exactly above it is the next
        message, and anything higher means something never arrived.
        """
        room = message.get("room")
        seq = message.get("seq", 0)
        cursor = self._cursors.get(room, 0)

        if seq <= cursor:
            return
        if seq > cursor + 1:
            # Dropping this copy is safe: the server stored it before
            # publishing, so the backfill about to be requested contains it.
            self._repair(room)
            return

        self._cursors[room] = seq
        self._append(room, message)
        self.on_change()

    def _repair(self, room):
        if room in self._repairing:
            return
        self._repairing.add(room)
        try:
            before = self._cursors.get(room, 0)
            reply = self.request("history", room=room, since=before)
            messages = reply.get("messages") or []

            # A backfill is capped at the *tail*, so a client far enough behind
            # gets the newest slice rather than the whole gap -- asking again
            # from the same cursor returns the same slice, so there is nothing
            # to loop over. What the cursor must not do is jump the shortfall in
            # silence: those messages were never delivered and nothing else
            # would ever mention them. So the gap is marked instead.
            if messages and messages[0]["seq"] > before + 1:
                missing = messages[0]["seq"] - before - 1
                self._append(
                    room,
                    {
                        "room": room,
                        "seq": messages[0]["seq"] - 1,
                        "author": "system",
                        "kind": "event",
                        "body": f"{missing} earlier message(s) not shown",
                        "at": messages[0]["at"],
                    },
                )

            for message in messages:
                if message["seq"] > self._cursors.get(room, 0):
                    self._cursors[room] = message["seq"]
                    self._append(room, message)
            self.on_change()
        except ChatError:
            # A repair that fails leaves the cursor where it was, so the next
            # message through re-detects the same gap and tries again.
            pass
        finally:
            self._repairing.discard(room)

    def _append(self, room, message):
        with self._lock:
            log = self.log.setdefault(room, [])
            log.append(message)
            if len(log) > LOG_LIMIT:
                del log[: len(log) - LOG_LIMIT]

            # A room is announced when its shape changes, not when it is spoken
            # in, so its `lastSeq` would otherwise be whatever it was at the
            # last invitation -- and every unread count computed from it wrong.
            space = self.rooms.get(room) or self.channels.get(room)
            if space is not None:
                space["lastSeq"] = max(space.get("lastSeq", 0), message.get("seq", 0))

    # -- operations -----------------------------------------------------------

    def sync(self):
        reply = self.request("sync")
        with self._lock:
            self.me = reply["me"]
            self.is_admin = reply.get("isAdmin", False)
            self.users = reply.get("users", [])
            self.groups = reply.get("groups", [])
            self.rooms = {room["id"]: room for room in reply.get("rooms", [])}
            self.channels = {c["id"]: c for c in reply.get("channels", [])}
            self.read = dict(reply.get("read") or {})
            self.connected = True

        # Backfill before announcing, so a view that draws on the change does
        # not have to draw again immediately for what it missed.
        for space in list(self.rooms.values()) + list(self.channels.values()):
            if space["lastSeq"] > self._cursors.get(space["id"], 0):
                self._repair(space["id"])
        self.on_change()
        return reply

    def send(self, room, body):
        return self.request("send", room=room, body=body)

    def open_room(self, invite=(), title=None, retention="persisted"):
        return self._track(
            self.request(
                "open", invite=list(invite), title=title, retention=retention
            )
        )

    def create_room(self, title, invite=()):
        return self._track(self.request("create", title=title, invite=list(invite)))

    def invite(self, room, principal):
        return self._track(self.request("invite", room=room, principal=principal)["room"])

    def uninvite(self, room, principal):
        return self._track(
            self.request("uninvite", room=room, principal=principal)["room"]
        )

    def leave(self, room):
        self.request("leave", room=room)
        self._forget(room)

    def enter(self, room):
        return self.request("enter", room=room)["occupancy"]

    def exit(self, occupancy):
        return self.request("exit", occupancy=occupancy)

    def mark_read(self, room, seq):
        if seq <= self.read.get(room, 0):
            return
        self.read[room] = seq
        self.request("read", room=room, seq=seq)

    def create_group(self, name, members=()):
        return self._track_group(
            self.request("group.create", name=name, members=list(members))
        )

    def assign_group(self, group, username):
        return self.request("group.assign", group=group, username=username)

    def unassign_group(self, group, username):
        return self.request("group.unassign", group=group, username=username)

    def subscribe(self, channel):
        return self._track(self.request("subscribe", channel=channel))

    def unsubscribe(self, channel):
        self.request("unsubscribe", channel=channel)
        self._forget(channel)

    # -- state ----------------------------------------------------------------

    def _track_group(self, group):
        """Record a group locally rather than waiting for its announcement.

        The push comes over the bus, so a caller that creates a group and
        immediately invites it would race its own announcement.
        """
        if not group or "id" not in group:
            return group
        with self._lock:
            self.groups = [g for g in self.groups if g["id"] != group["id"]] + [group]
            self.groups.sort(key=lambda g: g["name"])
        return group

    def _track(self, space):
        if not space or "id" not in space:
            return space
        with self._lock:
            target = self.channels if space.get("kind") == "channel" else self.rooms
            target[space["id"]] = space
        return space

    def _forget(self, space_id):
        with self._lock:
            self.rooms.pop(space_id, None)
            self.channels.pop(space_id, None)
            self.log.pop(space_id, None)
            self._cursors.pop(space_id, None)
            self.read.pop(space_id, None)

    def space(self, space_id):
        return self.rooms.get(space_id) or self.channels.get(space_id)

    def unread(self, space_id):
        """How far this person is behind in a space, as a count.

        Measured against the furthest sequence this client knows of, which is
        the delivery cursor when it is ahead of what the room last announced.
        The two answer different questions -- received, and seen -- and the gap
        between them is exactly what an unread count is.
        """
        space = self.space(space_id)
        if space is None:
            return 0
        known = max(space.get("lastSeq", 0), self._cursors.get(space_id, 0))
        return max(0, known - self.read.get(space_id, 0))

    def group_name(self, group_id):
        for group in self.groups:
            if group["id"] == group_id:
                return group["name"]
        return group_id

    def stop(self):
        self._stopping.set()
        self.socket.close()


def connect(base, username, password):
    """Log in and open a synced client. The whole handshake in one call."""
    from .transport import Http, Socket

    http = Http(base)
    profile = http.login(username, password)
    socket = Socket(base, http.cookie_header())
    socket.connect()

    client = ChatClient(socket)
    # The server sends its handshake first; syncing immediately is fine because
    # a reply is matched by pid rather than by arrival order.
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline and not socket.connected:
        time.sleep(0.05)
    client.sync()
    return http, client, profile
