import importlib
import io
import shutil
import sys
import tempfile
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))


@pytest.fixture
def run_dir():
    """A run directory short enough to hold an `ipc://` endpoint.

    A Unix socket path may not exceed 103 bytes, and `tmp_path` spends most of
    that on the test's own name before the bus appends its own -- which on
    macOS, where the temporary root is itself 50 characters, fails every bind.
    So the run directory sits at the short end of the temporary root, and is
    removed here rather than by pytest.
    """
    path = Path(tempfile.mkdtemp(prefix="minos-"))
    try:
        yield path
    finally:
        shutil.rmtree(path, ignore_errors=True)


@pytest.fixture
def app(tmp_path, run_dir, monkeypatch):
    """A server instance whose dist/ and vfs/ roots live under tmp_path."""
    dist = tmp_path / "dist"
    dist.mkdir()
    (dist / "index.html").write_text("<html>minos</html>")

    monkeypatch.setenv("MINOS_DIST", str(dist))
    monkeypatch.setenv("MINOS_VFS", str(tmp_path / "vfs"))
    monkeypatch.setenv("MINOS_RUN", str(run_dir))

    from server import config

    importlib.reload(config)
    from server import vfs as vfs_module

    importlib.reload(vfs_module)
    from server import sockets as sockets_module

    importlib.reload(sockets_module)
    from server import chat as chat_module

    importlib.reload(chat_module)
    from server import app as app_module

    importlib.reload(app_module)

    (tmp_path / "vfs").mkdir()
    instance = app_module.create_app()
    instance.config["TESTING"] = True
    yield instance

    # Each app owns a bus and, being first in its own MINOS_RUN, a proxy. Left
    # running they would pile up threads across the suite. The lease holds an
    # open lock file, so it is released here rather than waiting for atexit.
    handler = instance.extensions["chat"]
    handler.stop_sweeper()
    handler.bus.stop()
    handler.broker.stop()
    handler.lease.release()


@pytest.fixture
def client(app):
    return app.test_client()


@pytest.fixture
def auth(client):
    """A client with an authenticated demo session."""
    response = client.post("/login", json={"username": "demo", "password": "demo"})
    assert response.status_code == 200
    return client


def upload(client, path, data):
    return client.post(
        "/vfs/writefile",
        data={"path": path, "upload": (io.BytesIO(data), "upload")},
        content_type="multipart/form-data",
    )
