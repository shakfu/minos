import json

import pytest


class FakeWebsocket:
    """Records frames and replays a scripted sequence of receives."""

    def __init__(self, script=()):
        self.sent = []
        self.script = list(script)
        self.closed = None

    def send(self, frame):
        self.sent.append(json.loads(frame))

    def receive(self, timeout=None):
        if not self.script:
            raise StopIteration
        return self.script.pop(0)

    def close(self, code=None, reason=None):
        self.closed = (code, reason)

    def names(self):
        return [frame["name"] for frame in self.sent]


@pytest.fixture
def sockets(app):
    from server import sockets as module

    return module


@pytest.fixture
def registry(sockets):
    return sockets.Registry()


def connect(sockets, registry, user=None, ws=None):
    connection = sockets.Connection(ws or FakeWebsocket(), user or {"username": "demo"})
    registry.add(connection)
    return connection


def test_broadcast_reaches_every_connection(sockets, registry):
    first = connect(sockets, registry)
    second = connect(sockets, registry)

    assert registry.broadcast("osjs/vfs:watch:change", ["home:/"]) == 2
    assert first.ws.sent == second.ws.sent
    assert first.ws.sent[0] == {"name": "osjs/vfs:watch:change", "params": ["home:/"]}


def test_broadcast_to_user_skips_other_users(sockets, registry):
    mine = connect(sockets, registry, {"username": "demo"})
    theirs = connect(sockets, registry, {"username": "other"})

    assert registry.broadcast_to_user("demo", "osjs/vfs:watch:change") == 1
    assert mine.ws.names() == ["osjs/vfs:watch:change"]
    assert theirs.ws.sent == []


def test_broadcast_survives_a_dead_connection(sockets, registry):
    class Broken(FakeWebsocket):
        def send(self, frame):
            raise OSError("peer went away")

    connect(sockets, registry, ws=Broken())
    alive = connect(sockets, registry)

    assert registry.broadcast("osjs/core:ping") == 1
    assert alive.ws.names() == ["osjs/core:ping"]


def test_removed_connections_stop_receiving(sockets, registry):
    connection = connect(sockets, registry)
    registry.remove(connection)

    assert registry.broadcast("osjs/core:ping") == 0


@pytest.mark.parametrize(
    "frame",
    ["not json", "[]", '{"params": []}', '{"name": 1}', '{"name": "x", "params": "no"}'],
)
def test_malformed_frames_are_dropped(sockets, registry, frame):
    assert sockets.dispatch(registry, connect(sockets, registry), frame) is False


@pytest.mark.parametrize(
    "name",
    ["osjs/core:logged-in", "osjs/vfs:watch:change", "osjs/core:ping"],
)
def test_forged_internal_messages_are_refused(sockets, registry, name):
    called = []
    registry.register_application_handler("Textpad", lambda *a: called.append(a))
    frame = json.dumps({"name": name, "params": [{"pid": 1, "name": "Textpad", "args": []}]})

    assert sockets.dispatch(registry, connect(sockets, registry), frame) is False
    assert called == []


def test_application_message_without_a_handler_is_ignored(sockets, registry):
    frame = json.dumps(
        {"name": "osjs/application:socket:message", "params": [{"pid": 1, "name": "Nope"}]}
    )
    assert sockets.dispatch(registry, connect(sockets, registry), frame) is False


def test_application_message_reaches_its_handler(sockets, registry):
    seen = {}

    def handler(connection, respond, args):
        seen["args"] = args
        respond("pong", 42)

    registry.register_application_handler("Textpad", handler)
    connection = connect(sockets, registry)
    frame = json.dumps(
        {
            "name": "osjs/application:socket:message",
            "params": [{"pid": 7, "name": "Textpad", "args": ["ping"]}],
        }
    )

    assert sockets.dispatch(registry, connection, frame) is True
    assert seen["args"] == ["ping"]
    assert connection.ws.sent == [
        {
            "name": "osjs/application:socket:message",
            "params": [{"pid": 7, "args": ["pong", 42]}],
        }
    ]


def test_serve_announces_the_connection_then_cleans_up(sockets, registry):
    ws = FakeWebsocket()

    with pytest.raises(StopIteration):
        sockets.serve(registry, ws, {"username": "demo"}, ping_interval=30, session_max_age=1000)

    assert ws.sent[0] == {"name": "osjs/core:connected", "params": [{"cookie": {"maxAge": 1000}}]}
    assert len(registry) == 0


def test_serve_pings_when_the_client_is_silent(sockets, registry):
    ws = FakeWebsocket(script=[None, None])

    with pytest.raises(StopIteration):
        sockets.serve(registry, ws, {"username": "demo"}, ping_interval=30, session_max_age=1000)

    assert ws.names() == ["osjs/core:connected", "osjs/core:ping", "osjs/core:ping"]


def test_a_handler_that_raises_does_not_close_the_socket(sockets, registry):
    """A handler is reached by a client-supplied payload.

    Letting it raise would unwind through serve() and drop the connection, so
    one bad field would cost a client its socket instead of earning an error.
    """
    registry.register_application_handler("Boom", _raise)
    connection = connect(sockets, registry)
    frame = json.dumps(
        {
            "name": "osjs/application:socket:message",
            "params": [{"pid": 9, "name": "Boom", "args": []}],
        }
    )

    assert sockets.dispatch(registry, connection, frame) is True
    assert connection.ws.sent == [
        {"name": "osjs/application:socket:message", "params": [{"pid": 9, "args": [{"error": "Request failed"}]}]}
    ]
    assert len(registry) == 1


def _raise(connection, respond, args):
    raise ValueError("invalid literal for int()")


def test_a_handler_that_answers_then_raises_keeps_its_answer(sockets, registry):
    """The reply already sent is the caller's; the error is an extra frame.

    The client drops a pid the moment it resolves, so the second frame is
    ignored there rather than overwriting a good answer.
    """

    def answer_then_fail(connection, respond, args):
        respond({"ok": True})
        raise RuntimeError("after the fact")

    registry.register_application_handler("Late", answer_then_fail)
    connection = connect(sockets, registry)
    frame = json.dumps(
        {
            "name": "osjs/application:socket:message",
            "params": [{"pid": 4, "name": "Late", "args": []}],
        }
    )

    assert sockets.dispatch(registry, connection, frame) is True
    assert connection.ws.sent[0]["params"][0]["args"] == [{"ok": True}]


def test_handlers_are_scoped_to_one_application(sockets):
    """As a module global these were shared by every app in the process.

    A second application silently took over the first one's handlers, which is
    why the tests used to have to clear the map between cases.
    """
    first = sockets.Registry()
    second = sockets.Registry()

    first.register_application_handler("Only", lambda *a: None)

    assert "Only" in first.application_handlers
    assert second.application_handlers == {}


def test_a_frame_finds_no_handler_registered_on_another_registry(sockets):
    reached = []
    mine = sockets.Registry()
    theirs = sockets.Registry()
    theirs.register_application_handler("Textpad", lambda *a: reached.append(a))

    connection = connect(sockets, mine)
    frame = json.dumps(
        {
            "name": "osjs/application:socket:message",
            "params": [{"pid": 1, "name": "Textpad", "args": []}],
        }
    )

    assert sockets.dispatch(mine, connection, frame) is False
    assert reached == []


def test_a_broadcast_encodes_the_frame_once(monkeypatch):
    """Every recipient of a fan-out gets identical bytes.

    Encoding inside `Connection.send` would repeat the same `json.dumps` for
    each connection, which is the cost that grows with the size of a room
    rather than with the number of messages in it.
    """
    from server import sockets

    calls = []
    original = sockets.encode

    def counted(name, params=None):
        calls.append(name)
        return original(name, params)

    monkeypatch.setattr(sockets, "encode", counted)

    registry = sockets.Registry()
    for _ in range(5):
        registry.add(sockets.Connection(FakeWebsocket(), {"username": "demo"}))

    assert registry.broadcast("osjs/core:ping", [{"n": 1}]) == 5
    assert calls == ["osjs/core:ping"]


def test_a_broadcast_reaching_nobody_encodes_nothing(monkeypatch):
    from server import sockets

    calls = []
    monkeypatch.setattr(sockets, "encode", lambda *a, **k: calls.append(a) or "{}")

    registry = sockets.Registry()
    registry.add(sockets.Connection(FakeWebsocket(), {"username": "demo"}))

    assert registry.broadcast("x", predicate=lambda c: False) == 0
    assert calls == []
