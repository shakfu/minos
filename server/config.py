"""Server configuration for the minos backend."""

import os
from datetime import timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

DIST = Path(os.environ.get("MINOS_DIST", ROOT / "dist")).resolve()
VFS_ROOT = Path(os.environ.get("MINOS_VFS", ROOT / "vfs")).resolve()

HOST = os.environ.get("MINOS_HOST", "127.0.0.1")
PORT = int(os.environ.get("MINOS_PORT", "8000"))

# Session lifetime, refreshed on every request. The client is told this value
# during the websocket handshake and keeps it alive by pinging /ping.
SESSION_LIFETIME = timedelta(hours=12)

# Seconds of client silence before the server sends a keepalive frame.
WS_PING_INTERVAL = 30

# Poll dist/ and push hot-reload signals over the websocket.
WATCH_DIST = os.environ.get("MINOS_WATCH_DIST", "1") == "1"
WATCH_INTERVAL = 1.0

# Cookie signing key. Override in any deployment that is not a local demo.
SECRET_KEY = os.environ.get("MINOS_SECRET", "minos-development-secret")

# Demo credentials. Replace with a real adapter before exposing the server.
USERS = {"demo": "demo"}

# Files written into a home directory the first time its owner logs in.
HOME_TEMPLATE = {".desktop/.shortcuts.json": "[]"}

# Extra MIME types OS.js expects but that the stdlib table does not carry.
EXTRA_MIME_TYPES = {
    ".py": "text/x-python",
    ".ly": "text/x-lilypond",
    ".ily": "text/x-lilypond",
    ".tgz": "application/tar+gzip",
    ".md": "text/markdown",
}

MIME_BY_FILENAME = {"Makefile": "text/x-makefile", ".gitignore": "text/plain"}

SEARCH_LIMIT = 100
