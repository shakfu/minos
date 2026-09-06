"""A client that speaks the wire contract and nothing else.

Deliberately not `tui/`. That client is one of the two things under test here,
and a suite built on it would agree with the server about anything they had
both got wrong. This is written from docs/wire-contract.md instead: explicit
frames, explicit envelopes, no inference about what a field means.

Two objects. `Http` holds the session cookie and returns every response
including the failures, because a status code is part of the contract. `Socket`
runs a reader thread that sorts arriving frames into three places -- replies by
`pid`, chat pushes, and everything else -- so a test can wait for one kind
without draining another.
"""

import json
import queue
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from http.cookiejar import CookieJar

import simple_websocket

APPLICATION_MESSAGE = "osjs/application:socket:message"
APPLICATION = "Chat"

# How long to wait for a reply to an operation, and for a push that a test says
# must arrive. Local round trips; the ceiling turns a lost frame into a failure
# rather than a hang.
REPLY_TIMEOUT = 10.0
PUSH_TIMEOUT = 10.0

# How long a socket waits for its handshake before the connection is a failure.
HANDSHAKE_TIMEOUT = 10.0

# How long to wait for the handshake before provoking the server into sending
# something else. See Socket.handshake for what this works around.
PROVOKE_AFTER = 0.5


class WireError(Exception):
    """The server refused an operation, or never answered one."""


class Response:
    """One HTTP answer, successful or not."""

    def __init__(self, status, headers, body):
        self.status = status
        self.headers = headers
        self.body = body

    @property
    def text(self):
        return self.body.decode(errors="replace")

    @property
    def json(self):
        return json.loads(self.body) if self.body else None

    @property
    def error(self):
        """The `error` field, for a refusal. None when the body is not one."""
        try:
            payload = self.json
        except ValueError:
            return None
        return payload.get("error") if isinstance(payload, dict) else None


class Http:
    """An HTTP session against one server, holding its cookie."""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.jar = CookieJar()
        self._opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )

    def call(self, method, path, payload=None, params=None, body=None, headers=None):
        """One request. Returns the response whatever its status."""
        url = f"{self.base}{path}"
        if params:
            url = f"{url}?{urllib.parse.urlencode(params)}"

        headers = dict(headers or {})
        if payload is not None:
            body = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"

        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=15) as response:
                return Response(response.status, dict(response.headers), response.read())
        except urllib.error.HTTPError as error:
            return Response(error.code, dict(error.headers), error.read())

    # -- the routes -----------------------------------------------------------

    def ping(self):
        return self.call("GET", "/ping")

    def login(self, username, password):
        return self.call("POST", "/login", {"username": username, "password": password})

    def logout(self):
        return self.call("POST", "/logout", {})

    def settings(self):
        return self.call("GET", "/settings")

    def put_settings(self, payload):
        return self.call("POST", "/settings", payload)

    def vfs(self, method, **fields):
        """A VFS call, sent the way the contract says that method is sent."""
        if method in GET_METHODS:
            params = {key: value for key, value in fields.items() if value is not None}
            if "options" in params and not isinstance(params["options"], str):
                params["options"] = json.dumps(params["options"])
            return self.call("GET", f"/vfs/{method}", params=params)
        return self.call("POST", f"/vfs/{method}", fields)

    def upload(self, path, data, filename="upload"):
        """`writefile`, which is the one multipart route."""
        boundary = uuid.uuid4().hex
        parts = [
            f'--{boundary}\r\nContent-Disposition: form-data; name="path"\r\n\r\n{path}\r\n'.encode(),
            (
                f'--{boundary}\r\nContent-Disposition: form-data; name="upload";'
                f' filename="{filename}"\r\n'
                "Content-Type: application/octet-stream\r\n\r\n"
            ).encode(),
            data,
            f"\r\n--{boundary}--\r\n".encode(),
        ]
        return self.call(
            "POST",
            "/vfs/writefile",
            body=b"".join(parts),
            headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
        )

    def cookie_header(self):
        return "; ".join(f"{cookie.name}={cookie.value}" for cookie in self.jar)


# Methods the contract sends as GET with query parameters.
GET_METHODS = {"capabilities", "exists", "stat", "readdir", "readfile"}


class Socket:
    """One websocket, with its frames sorted by a reader thread.

    `replies` are matched by the `pid` a request was sent with. `pushes` holds
    the chat events, which arrive with a null pid. `control` holds every other
    frame -- the handshake and the keepalive -- so a test can assert on those
    without competing with the chat traffic.
    """

    def __init__(self, base, cookie=None):
        self.url = _websocket_url(base)
        self.cookie = cookie
        self.pushes = queue.Queue()
        self.control = queue.Queue()
        self.closed = threading.Event()
        self.close_reason = None

        self._socket = None
        self._reader = None
        self._send_lock = threading.Lock()
        self._pid = 0
        self._replies = {}
        self._lock = threading.Lock()

    # -- lifecycle ------------------------------------------------------------

    def connect(self):
        headers = {"Cookie": self.cookie} if self.cookie else None
        self._socket = simple_websocket.Client(self.url, headers=headers)
        self._reader = threading.Thread(target=self._read, daemon=True)
        self._reader.start()
        return self

    def _read(self):
        while not self.closed.is_set():
            try:
                raw = self._socket.receive(timeout=0.5)
            except simple_websocket.ConnectionClosed as closed:
                self.close_reason = (closed.reason, closed.message)
                break
            except Exception:
                break
            if raw is None:
                continue
            try:
                self._sort(json.loads(raw))
            except ValueError:
                continue
        self.closed.set()
        self._fail_pending()

    def _sort(self, frame):
        if frame.get("name") != APPLICATION_MESSAGE:
            self.control.put(frame)
            return

        params = frame.get("params") or []
        envelope = params[0] if params and isinstance(params[0], dict) else {}
        args = envelope.get("args") or []

        if envelope.get("pid") is None:
            self.pushes.put(args[0] if args else None)
            return

        # Left in place rather than popped: `await_reply` is what removes it,
        # so a caller may wait for a reply that has already arrived.
        with self._lock:
            slot = self._replies.get(envelope["pid"])
        if slot is not None:
            slot["reply"] = args[0] if args else None
            slot["event"].set()

    def _fail_pending(self):
        with self._lock:
            waiting = list(self._replies.values())
            self._replies.clear()
        for slot in waiting:
            slot["reply"] = {"error": "Disconnected"}
            slot["event"].set()

    def close(self):
        self.closed.set()
        if self._socket is not None:
            try:
                self._socket.close()
            except Exception:
                pass
            self._socket = None
        if self._reader is not None:
            self._reader.join(timeout=2)

    # -- sending --------------------------------------------------------------

    def send_frame(self, frame):
        """Whatever this is, on the wire. For the tests about malformed input."""
        raw = frame if isinstance(frame, str) else json.dumps(frame)
        with self._send_lock:
            self._socket.send(raw)

    def send_op(self, op, **body):
        """One operation, returning the pid it went out with."""
        with self._lock:
            self._pid += 1
            pid = self._pid
            self._replies[pid] = {"event": threading.Event(), "reply": None}
        self.send_frame(
            {
                "name": APPLICATION_MESSAGE,
                "params": [{"pid": pid, "name": APPLICATION, "args": [{"op": op, **body}]}],
            }
        )
        return pid

    def await_reply(self, pid, timeout=REPLY_TIMEOUT):
        with self._lock:
            slot = self._replies.get(pid)
        if slot is None:
            raise WireError(f"No request outstanding for pid {pid}")
        answered = slot["event"].wait(timeout)
        with self._lock:
            self._replies.pop(pid, None)
        if not answered:
            raise WireError(f"No reply to pid {pid} within {timeout}s")
        return slot["reply"]

    def call(self, op, **body):
        """One operation, raising on a refusal. The common case."""
        reply = self.await_reply(self.send_op(op, **body))
        if isinstance(reply, dict) and "error" in reply:
            raise WireError(reply["error"])
        return reply

    def refuse(self, op, **body):
        """One operation that must be refused. Returns the message."""
        reply = self.await_reply(self.send_op(op, **body))
        if not isinstance(reply, dict) or "error" not in reply:
            raise AssertionError(f"{op} was not refused: {reply!r}")
        return reply["error"]

    # -- receiving ------------------------------------------------------------

    def handshake(self, timeout=HANDSHAKE_TIMEOUT):
        """The `osjs/core:connected` frame, which arrives before anything else.

        A server fast enough to put that frame in the same TCP segment as the
        101 response exposes a defect in `simple_websocket`: its reader blocks
        on the socket before draining what its parser already holds, so a frame
        that arrived alongside the handshake response waits for unrelated
        traffic that may never come. Provoking a reply is what supplies it.

        This weakens nothing. The assertion is still that
        `osjs/core:connected` is the first control frame on the connection.
        """
        try:
            frame = _drain(self.control, lambda f: True, PROVOKE_AFTER)
        except AssertionError:
            self._provoke()
            frame = _drain(self.control, lambda f: True, timeout)

        if frame["name"] != "osjs/core:connected":
            raise AssertionError(f"First frame was {frame['name']!r}")
        return frame

    def _provoke(self):
        """Ask something answerable, so that bytes come back.

        The reply is discarded: it carries a pid no caller is waiting on, and
        its only job is to be inbound traffic.
        """
        self.send_frame(
            {
                "name": APPLICATION_MESSAGE,
                "params": [{"pid": 0, "name": APPLICATION, "args": [{"op": "__wake__"}]}],
            }
        )

    def expect_push(self, predicate, timeout=PUSH_TIMEOUT):
        """The next push matching `predicate`. Discards the ones before it."""
        return _drain(self.pushes, predicate, timeout)

    def collect_push(self, predicate, timeout=PUSH_TIMEOUT):
        """As `expect_push`, but also returns what came before the match.

        The only way to prove a push did *not* arrive: wait for one that must,
        and inspect everything that overtook it.
        """
        seen = []
        return _drain(self.pushes, predicate, timeout, seen), seen

    def expect_control(self, predicate, timeout=PUSH_TIMEOUT):
        return _drain(self.control, predicate, timeout)

    def drain(self):
        """Discard everything queued. Use before an action whose pushes matter."""
        for channel in (self.pushes, self.control):
            while True:
                try:
                    channel.get_nowait()
                except queue.Empty:
                    break


def _drain(channel, predicate, timeout, seen=None):
    """Pull from a queue until something matches, or the deadline passes."""
    deadline = time.monotonic() + timeout
    seen = [] if seen is None else seen
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise AssertionError(f"No matching frame in {timeout}s; saw {seen!r}")
        try:
            item = channel.get(timeout=min(remaining, 0.25))
        except queue.Empty:
            continue
        if predicate(item):
            return item
        seen.append(item)


def _websocket_url(base):
    parts = urllib.parse.urlsplit(base)
    scheme = "wss" if parts.scheme == "https" else "ws"
    return urllib.parse.urlunsplit((scheme, parts.netloc, "/", "", ""))


def push_of(kind):
    """A predicate for `expect_push`: the next push of this type."""
    return lambda event: isinstance(event, dict) and event.get("type") == kind


def message_in(room_id):
    def matches(event):
        return (
            isinstance(event, dict)
            and event.get("type") == "message"
            and event.get("room") == room_id
        )

    return matches
