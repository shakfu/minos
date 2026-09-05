"""Talking to a minos server from outside a browser.

Two connections, the same two the web client makes: an HTTP session that holds
the cookie, and one websocket that carries every named frame. Neither needs a
dependency the server does not already have -- `urllib` keeps a cookie jar, and
`simple_websocket` is the client `flask-sock` is built on and the test suite
already drives.

The frozen wire format is the browser's: reads are GETs with query parameters,
writes are JSON POSTs, and the socket carries `{name, params}` frames. Nothing
about it assumes a browser, which is what makes this possible at all.
"""

import json
import threading
import urllib.error
import urllib.parse
import urllib.request
from http.cookiejar import CookieJar

import simple_websocket

APPLICATION_MESSAGE = "osjs/application:socket:message"


class TransportError(Exception):
    """A request the server refused, or a connection that would not open."""


class Http:
    """An HTTP session against one server, holding the login cookie.

    A cookie jar rather than a header we manage: the server sets a rolling
    session cookie and expects it back on every request, and `urllib` already
    knows how to do that correctly.
    """

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.jar = CookieJar()
        self._opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )

    def _request(self, method, path, data=None, params=None):
        url = f"{self.base}{path}"
        if params:
            url = f"{url}?{urllib.parse.urlencode(params)}"

        body = None
        headers = {}
        if data is not None:
            body = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"

        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=15) as response:
                raw = response.read()
        except urllib.error.HTTPError as error:
            detail = error.read().decode(errors="replace")
            try:
                detail = json.loads(detail).get("error", detail)
            except ValueError:
                pass
            raise TransportError(f"{error.code}: {detail}") from error
        except OSError as error:
            raise TransportError(f"Cannot reach {self.base}: {error}") from error

        return json.loads(raw) if raw else None

    def login(self, username, password):
        return self._request(
            "POST", "/login", {"username": username, "password": password}
        )

    def logout(self):
        return self._request("POST", "/logout", {})

    def ping(self):
        return self._request("GET", "/ping")

    def cookie_header(self):
        """The session cookie, formatted for the websocket upgrade.

        The upgrade is a plain HTTP request and the server gates it on the
        session, so the cookie has to travel by hand: `simple_websocket` takes
        headers but has no jar of its own.
        """
        pairs = [f"{cookie.name}={cookie.value}" for cookie in self.jar]
        return "; ".join(pairs)


class Socket:
    """The core websocket, with a reader thread behind a callback.

    Frames arrive on their own thread and are handed straight to `on_frame`;
    the caller decides what is a reply and what is a push. Sends are locked
    because the UI thread and the keepalive both use them.
    """

    def __init__(self, base, cookie):
        self.url = _websocket_url(base)
        self.cookie = cookie
        self._socket = None
        self._reader = None
        self._send_lock = threading.Lock()
        self._closing = threading.Event()
        self.on_frame = lambda frame: None
        self.on_close = lambda: None

    @property
    def connected(self):
        return self._socket is not None and not self._closing.is_set()

    def connect(self):
        headers = {"Cookie": self.cookie} if self.cookie else None
        try:
            self._socket = simple_websocket.Client(self.url, headers=headers)
        except Exception as error:  # simple_websocket raises several shapes
            raise TransportError(f"Cannot open a socket to {self.url}: {error}") from error

        self._closing.clear()
        self._reader = threading.Thread(target=self._read, name="ws-reader", daemon=True)
        self._reader.start()

    def _read(self):
        while not self._closing.is_set():
            try:
                raw = self._socket.receive(timeout=1)
            except Exception:
                break
            if raw is None:
                continue  # A timeout, not a close: the server pings when quiet.
            try:
                frame = json.loads(raw)
            except ValueError:
                continue
            self.on_frame(frame)

        if not self._closing.is_set():
            self._closing.set()
            self.on_close()

    def send(self, name, *params):
        if not self.connected:
            raise TransportError("Not connected")
        frame = json.dumps({"name": name, "params": list(params)})
        with self._send_lock:
            self._socket.send(frame)

    def close(self):
        self._closing.set()
        if self._socket is not None:
            try:
                self._socket.close()
            except Exception:
                pass
            self._socket = None


def _websocket_url(base):
    parts = urllib.parse.urlsplit(base)
    scheme = "wss" if parts.scheme == "https" else "ws"
    return urllib.parse.urlunsplit((scheme, parts.netloc, "/", "", ""))
