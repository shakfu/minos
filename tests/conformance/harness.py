"""Launching a server under test, and waiting until it answers.

Nothing here knows what the server is written in. It receives an argv and a
directory to keep state in, starts it, and polls `/ping` until the port is
live. `MINOS_CONFORMANCE_CMD` replaces the argv; `MINOS_CONFORMANCE_URL` names
a server that is already running, in which case nothing is launched at all.
"""

import os
import shlex
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent

# How long a server gets to bind its port and answer. Generous: a first run
# creates a database and, for the Python server, imports Flask.
STARTUP_TIMEOUT = 20.0
STARTUP_POLL = 0.05

# How long a terminated server gets to exit before it is killed.
SHUTDOWN_TIMEOUT = 5.0

DEFAULT_COMMAND = [sys.executable, "-m", "server.app"]


def command():
    """The argv to launch, from the environment or the Python server."""
    override = os.environ.get("MINOS_CONFORMANCE_CMD")
    return shlex.split(override) if override else list(DEFAULT_COMMAND)


def external_url():
    """A server the operator is running themselves, if there is one."""
    return os.environ.get("MINOS_CONFORMANCE_URL")


def free_port():
    """A port nothing is listening on.

    Racy in principle -- something could take it between the close and the
    server's bind -- and adequate in practice for a local test run. The
    alternative is asking the server what it bound, which would mean the
    contract had to include a way to ask.
    """
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


class Server:
    """One server process, and the base URL that reaches it."""

    def __init__(self, base, process=None, log=None):
        self.base = base
        self.process = process
        self.log = log

    def stop(self):
        if self.process is None:
            return
        self.process.terminate()
        try:
            self.process.wait(timeout=SHUTDOWN_TIMEOUT)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=SHUTDOWN_TIMEOUT)

    def output(self):
        """Whatever the server printed. Only read when something has failed."""
        if self.log is None or not self.log.exists():
            return ""
        return self.log.read_text(errors="replace")


def launch(state_dir, **settings):
    """Start a server on a free port with its state under `state_dir`.

    `settings` are extra environment variables, which is how a test asks for a
    short room grace or a fast keepalive. The names are the contract's; see
    docs/wire-contract.md.
    """
    state_dir = Path(state_dir)
    port = free_port()

    environment = dict(os.environ)
    environment.update(
        {
            "MINOS_HOST": "127.0.0.1",
            "MINOS_PORT": str(port),
            "MINOS_RUN": str(state_dir / "run"),
            "MINOS_VFS": str(state_dir / "vfs"),
            "MINOS_DIST": str(state_dir / "dist"),
        }
    )
    environment.update({name: str(value) for name, value in settings.items()})

    # A built client is optional, but the index route is part of the contract,
    # so the suite provides one rather than testing a 404 it did not intend.
    dist = state_dir / "dist"
    dist.mkdir(parents=True, exist_ok=True)
    (dist / "index.html").write_text("<html>minos</html>")

    log = state_dir / "server.log"
    handle = log.open("wb")
    process = subprocess.Popen(
        command(), cwd=ROOT, env=environment, stdout=handle, stderr=subprocess.STDOUT
    )

    server = Server(f"http://127.0.0.1:{port}", process, log)
    try:
        await_ready(server)
    except Exception:
        server.stop()
        raise
    return server


def await_ready(server):
    """Poll `/ping` until the server answers, or give up with its output."""
    deadline = time.monotonic() + STARTUP_TIMEOUT
    while time.monotonic() < deadline:
        if server.process is not None and server.process.poll() is not None:
            raise RuntimeError(
                f"Server exited with {server.process.returncode}:\n{server.output()}"
            )
        try:
            with urllib.request.urlopen(f"{server.base}/ping", timeout=1) as response:
                if response.status == 200:
                    return server
        except (urllib.error.URLError, OSError, TimeoutError):
            time.sleep(STARTUP_POLL)
    raise RuntimeError(
        f"Server did not answer /ping within {STARTUP_TIMEOUT}s:\n{server.output()}"
    )
