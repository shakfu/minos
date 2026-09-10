"""Server configuration for the minos backend."""

import os
from datetime import timedelta
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

DIST = Path(os.environ.get("MINOS_DIST", ROOT / "dist")).resolve()
VFS_ROOT = Path(os.environ.get("MINOS_VFS", ROOT / "vfs")).resolve()

# Runtime state that is neither source nor user data: the message bus sockets
# and the timeline database. Generated, and safe to delete when nothing runs.
RUN_DIR = Path(os.environ.get("MINOS_RUN", ROOT / ".run")).resolve()

HOST = os.environ.get("MINOS_HOST", "127.0.0.1")
PORT = int(os.environ.get("MINOS_PORT", "8000"))

# Session lifetime, refreshed on every request. The client is told this value
# during the websocket handshake and keeps it alive by pinging /ping.
SESSION_LIFETIME = timedelta(hours=12)

# Seconds of client silence before the server sends a keepalive frame.
# Overridable so a conformance run can observe one without waiting half a minute.
WS_PING_INTERVAL = float(os.environ.get("MINOS_WS_PING", "30"))

# Cookie signing key. The default is usable, which is only safe because this
# server runs locally; see USERS below.
SECRET_KEY = os.environ.get("MINOS_SECRET", "minos-development-secret")

# Test credentials, in plain text, and a fixture rather than a placeholder: this
# server is not to be exposed, so there is no adapter coming. Anything that
# needs real accounts needs a real user store and a secret with no default.
# Three of them, because one user cannot demonstrate a conversation.
USERS = {"demo": "demo", "alice": "alice", "bob": "bob"}

# Who may found a permanent room, invite to one, and manage groups. The name is
# carried on the session profile's `groups`, so the rest of the server asks the
# profile rather than this set. A demo needs at least one, or nothing
# institutional can be created at all.
ADMINS = {"demo"}

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

# Timeline store. Rooms, membership and messages, with the per-room sequence
# numbers the client uses to detect a gap. SQLite rather than a file in the VFS:
# the sequence has to be assigned atomically, and more than one worker process
# writes here.
TIMELINE_DB = Path(os.environ.get("MINOS_TIMELINE_DB", RUN_DIR / "timeline.db"))

# Message bus endpoints. Producers PUB into XSUB, subscribers SUB from XPUB, and
# a proxy joins the two; whichever process starts first binds them. ipc:// keeps
# the bus on the filesystem where directory permissions scope it, which tcp on
# loopback would not; override both for a bus that spans hosts.
BUS_XSUB = os.environ.get("MINOS_BUS_XSUB", f"ipc://{RUN_DIR / 'bus-xsub'}")
BUS_XPUB = os.environ.get("MINOS_BUS_XPUB", f"ipc://{RUN_DIR / 'bus-xpub'}")

# Messages returned for a room when the client has no cursor, and the ceiling on
# any single backfill.
HISTORY_LIMIT = 200

# The channel every user subscribes to, carrying machine events rather than
# typed messages. A channel rather than a room: the server is its only producer
# and its audience may only read.
SYSTEM_CHANNEL = "system"

# How long a transient room outlives its last occupant. Short enough that the
# promise of deletion means something, long enough that a reload or a dropped
# connection does not destroy a live conversation. Users are relying on this,
# so it is documented rather than merely configured.
ROOM_GRACE = float(os.environ.get("MINOS_ROOM_GRACE", "120"))

# How often the sweep looks for rooms whose grace has run out, and therefore
# the resolution of the promise above: a room goes between ROOM_GRACE and
# ROOM_GRACE + ROOM_SWEEP seconds after emptying.
ROOM_SWEEP = float(os.environ.get("MINOS_ROOM_SWEEP", "15"))
