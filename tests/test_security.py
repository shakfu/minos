import pytest

from conftest import upload


@pytest.mark.parametrize(
    "path",
    [
        "home:/../../etc/passwd",
        "home:/subdir/../../../outside.txt",
        "home:/./../escape",
    ],
)
def test_traversal_out_of_a_mountpoint_is_refused(auth, path):
    assert auth.get("/vfs/stat", query_string={"path": path}).status_code == 403


def test_symlink_out_of_a_mountpoint_is_refused(auth, tmp_path):
    secret = tmp_path / "secret.txt"
    secret.write_text("classified")
    (tmp_path / "vfs" / "demo" / "link.txt").symlink_to(secret)

    assert auth.get("/vfs/readfile", query_string={"path": "home:/link.txt"}).status_code == 403


def test_unknown_mountpoint_is_404(auth):
    assert auth.get("/vfs/stat", query_string={"path": "nope:/x"}).status_code == 404


def test_malformed_path_is_rejected(auth):
    assert auth.get("/vfs/stat", query_string={"path": "no-mountpoint"}).status_code == 400


def test_osjs_mountpoint_is_readable(auth):
    listing = auth.get("/vfs/readdir", query_string={"path": "osjs:/"}).get_json()
    assert "index.html" in [entry["filename"] for entry in listing]


def test_osjs_mountpoint_refuses_writes(auth):
    assert auth.post("/vfs/mkdir", json={"path": "osjs:/evil"}).status_code == 403
    assert upload(auth, "osjs:/evil.txt", b"x").status_code == 403
