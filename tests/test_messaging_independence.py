"""The messaging layer must not depend on the server it happens to live beside.

An extraction that still reaches back into the application is not an extraction.
These are the checks that keep it honest: no imports of `server`, no Flask, and
a working conversation assembled without any of it.
"""

import ast
import subprocess
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
PACKAGE = ROOT / "messaging"

FORBIDDEN = {"server", "flask", "flask_sock", "werkzeug"}


def module_paths():
    return sorted(PACKAGE.glob("*.py"))


def imported_roots(path):
    """Every top-level package name this module imports."""
    tree = ast.parse(path.read_text())
    roots = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            roots.update(alias.name.split(".")[0] for alias in node.names)
        elif isinstance(node, ast.ImportFrom):
            # A relative import stays inside the package by definition.
            if node.level == 0 and node.module:
                roots.add(node.module.split(".")[0])
    return roots


def test_the_package_has_modules_to_check():
    assert {path.name for path in module_paths()} >= {
        "__init__.py",
        "bus.py",
        "service.py",
        "timeline.py",
    }


@pytest.mark.parametrize("path", module_paths(), ids=lambda p: p.name)
def test_no_module_imports_the_application(path):
    assert imported_roots(path) & FORBIDDEN == set()


def test_the_package_imports_without_the_server_on_the_path():
    """Imported from a bare interpreter, with only the repository root visible.

    A subprocess rather than an assertion about `sys.modules`, because the test
    session has already imported the server and would hide the coupling.
    """
    result = subprocess.run(
        [
            sys.executable,
            "-c",
            "import messaging;"
            "assert not [m for m in __import__('sys').modules if m.startswith('server')],"
            " 'importing messaging pulled in the server'",
        ],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr


def test_a_conversation_works_with_no_server_at_all(tmp_path):
    """The module's own wiring, exercised end to end.

    Delivery goes to a list instead of a websocket and the roster is a literal
    set, which is the whole point: nothing here knows what is carrying it.
    """
    import messaging

    delivered = []

    timeline = messaging.Timeline(db_path=tmp_path / "t.db", run_dir=tmp_path).init()
    bus = messaging.Bus(f"ipc://{tmp_path}/xsub", f"ipc://{tmp_path}/xpub")

    service = messaging.Messaging(
        timeline,
        bus,
        deliver=lambda audience, event: delivered.append((audience, event)),
        roster=lambda: {"ada", "grace"},
    )

    room = service.open_room("ada", invitees=["grace"], title="Pair")
    assert room["audience"] == ["ada", "grace"]

    assert service.send("ada", room["id"], "hello")["seq"] == 1
    assert service.send("grace", room["id"], "hi")["seq"] == 2

    reply = service.history("grace", room["id"], since=0)
    assert [m["body"] for m in reply["messages"]] == ["hello", "hi"]
    assert reply["lastSeq"] == 2

    with pytest.raises(messaging.MessagingError):
        service.send("mallory", room["id"], "let me in")


def test_a_channel_is_a_room_with_a_producer(tmp_path):
    import messaging

    timeline = messaging.Timeline(db_path=tmp_path / "t.db", run_dir=tmp_path).init()
    bus = messaging.Bus(f"ipc://{tmp_path}/xsub", f"ipc://{tmp_path}/xpub")
    service = messaging.Messaging(
        timeline, bus, deliver=lambda *a: None, roster=lambda: {"ada"}
    )

    channel = service.ensure_channel("system", "System", ["ada"])
    assert channel["kind"] == messaging.CHANNEL
    assert channel["audience"] == ["ada"]

    # Declaring it again is the same channel, so a host can do it on every boot.
    assert service.ensure_channel("system", "System", ["ada"])["id"] == channel["id"]

    # A subscriber reads; writing is the producer's alone.
    with pytest.raises(messaging.MessagingError):
        service.send("ada", channel["id"], "can I post here")


def test_two_stores_do_not_share_state(tmp_path):
    """The store is an instance, so a host can run more than one."""
    import messaging

    first = messaging.Timeline(db_path=tmp_path / "one.db", run_dir=tmp_path).init()
    second = messaging.Timeline(db_path=tmp_path / "two.db", run_dir=tmp_path).init()

    room = first.create_room("Only in the first", created_by="ada")

    assert first.room(room["id"]) is not None
    assert second.room(room["id"]) is None
