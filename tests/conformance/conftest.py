"""Fixtures for the conformance suite.

One server for the whole session. A black-box server holds durable state, so
isolating tests would mean restarting it between them, and a restart costs more
than the entire suite. The rule that replaces isolation: **no test may assume
it is alone.** Address rooms by the id the server returned, suffix any name
that has to be unique, and never count anything server-wide.

`fresh_server` is the exception, for behaviour that only happens at start-up or
that needs the clock moved.
"""

import uuid

import pytest

from . import harness
from .wire import Http, Socket

# The demo accounts the contract names. `demo` is the only administrator.
ADMIN = "demo"
PASSWORDS = {"demo": "demo", "alice": "alice", "bob": "bob"}


@pytest.fixture(scope="session")
def server(tmp_path_factory):
    """The server under test, launched once."""
    external = harness.external_url()
    if external:
        yield harness.await_ready(harness.Server(external))
        return

    instance = harness.launch(tmp_path_factory.mktemp("conformance"))
    try:
        yield instance
    finally:
        instance.stop()


@pytest.fixture
def fresh_server(tmp_path_factory):
    """A factory for servers of one's own, with settings from the contract."""
    if harness.external_url():
        pytest.skip("A server was supplied; this test must launch its own")

    started = []

    def launch(**settings):
        instance = harness.launch(
            tmp_path_factory.mktemp("conformance-fresh"), **settings
        )
        started.append(instance)
        return instance

    try:
        yield launch
    finally:
        for instance in started:
            instance.stop()


@pytest.fixture
def anonymous(server):
    """An HTTP session that has not logged in."""
    return Http(server.base)


@pytest.fixture
def session(server):
    """A factory for logged-in HTTP sessions."""

    def login(username):
        http = Http(server.base)
        response = http.login(username, PASSWORDS[username])
        assert response.status == 200, response.text
        return http

    return login


@pytest.fixture
def attach():
    """A factory for connected sockets against any server, synced and closed.

    Takes the server because `fresh_server` hands out its own; `connect` is the
    same thing bound to the one under test.
    """
    opened = []

    def open_socket(instance, username):
        http = Http(instance.base)
        response = http.login(username, PASSWORDS[username])
        assert response.status == 200, response.text

        socket = Socket(instance.base, http.cookie_header()).connect()
        socket.handshake()
        socket.call("sync")
        opened.append(socket)
        return socket

    try:
        yield open_socket
    finally:
        for socket in opened:
            socket.close()


@pytest.fixture
def connect(server, attach):
    """A factory for connected sockets against the server under test."""
    return lambda username: attach(server, username)


@pytest.fixture
def demo(connect):
    """The administrator, connected and synced."""
    return connect(ADMIN)


@pytest.fixture
def alice(connect):
    return connect("alice")


@pytest.fixture
def bob(connect):
    return connect("bob")


@pytest.fixture
def unique():
    """A name no other test will have used, for anything server-wide unique."""

    def name(prefix="x"):
        return f"{prefix}-{uuid.uuid4().hex[:10]}"

    return name
