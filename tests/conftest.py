import importlib
import io
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))


@pytest.fixture
def app(tmp_path, monkeypatch):
    """A server instance whose dist/ and vfs/ roots live under tmp_path."""
    dist = tmp_path / "dist"
    dist.mkdir()
    (dist / "index.html").write_text("<html>minos</html>")

    monkeypatch.setenv("MINOS_DIST", str(dist))
    monkeypatch.setenv("MINOS_VFS", str(tmp_path / "vfs"))
    monkeypatch.setenv("MINOS_RUN", str(tmp_path / "run"))

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
