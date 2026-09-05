"""End-to-end checks against a real HTTP server and a real websocket client.

The unit tests drive the socket module directly; these prove the route is
actually reachable at `/` alongside the index route, and that the session
cookie gates the upgrade.
"""

import json
import threading
import urllib.request

import pytest
import simple_websocket
from werkzeug.serving import make_server


@pytest.fixture
def server(app):
    instance = make_server("127.0.0.1", 0, app, threaded=True)
    thread = threading.Thread(target=instance.serve_forever, daemon=True)
    thread.start()
    yield f"127.0.0.1:{instance.server_port}"
    instance.shutdown()
    thread.join(timeout=5)


def login(server):
    """Log in over HTTP and return the session cookie for the upgrade request."""
    request = urllib.request.Request(
        f"http://{server}/login",
        data=json.dumps({"username": "demo", "password": "demo"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request) as response:
        return response.headers["Set-Cookie"].split(";")[0]


def open_socket(server, cookie):
    return simple_websocket.Client(
        f"ws://{server}/", headers={"Cookie": cookie} if cookie else None
    )


def test_handshake_announces_the_connection(server):
    client = open_socket(server, login(server))
    try:
        frame = json.loads(client.receive(timeout=5))
    finally:
        client.close()

    assert frame["name"] == "osjs/core:connected"
    assert frame["params"][0]["cookie"]["maxAge"] > 0


def test_an_anonymous_upgrade_is_refused(server):
    """The upgrade completes, then the server closes it with a policy violation."""
    client = open_socket(server, None)

    with pytest.raises(simple_websocket.ConnectionClosed) as closed:
        for _ in range(5):
            client.receive(timeout=5)

    assert closed.value.reason == 1008
    assert closed.value.message == "Not authenticated"


def test_a_broadcast_reaches_a_live_client(app, server):
    client = open_socket(server, login(server))
    try:
        # The handshake is sent after the connection is registered, so
        # receiving it means the broadcast below cannot miss it.
        client.receive(timeout=5)
        assert len(app.extensions["sockets"]) == 1
        app.extensions["sockets"].broadcast("osjs/vfs:watch:change", ["home:/"])

        frame = json.loads(client.receive(timeout=5))
    finally:
        client.close()

    assert frame == {"name": "osjs/vfs:watch:change", "params": ["home:/"]}


def test_an_application_message_round_trips(app, server):
    registry = app.extensions["sockets"]
    registry.register_application_handler(
        "Echo", lambda connection, respond, args: respond(*args)
    )
    client = open_socket(server, login(server))
    try:
        client.receive(timeout=5)  # handshake
        client.send(
            json.dumps(
                {
                    "name": "osjs/application:socket:message",
                    "params": [{"pid": 3, "name": "Echo", "args": ["hello"]}],
                }
            )
        )
        frame = json.loads(client.receive(timeout=5))
    finally:
        client.close()
        registry.application_handlers.pop("Echo", None)

    assert frame == {
        "name": "osjs/application:socket:message",
        "params": [{"pid": 3, "args": ["hello"]}],
    }


def test_the_index_route_still_answers_on_the_same_path(server):
    with urllib.request.urlopen(f"http://{server}/") as response:
        assert b"minos" in response.read()
