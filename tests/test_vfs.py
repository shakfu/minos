import json

from conftest import upload


def readdir(client, path="home:/"):
    return client.get("/vfs/readdir", query_string={"path": path}).get_json()


def test_readdir_returns_client_file_descriptors(auth):
    upload(auth, "home:/notes.txt", b"hello")
    entry = next(e for e in readdir(auth) if e["filename"] == "notes.txt")

    assert entry["path"] == "home:/notes.txt"
    assert entry["isFile"] is True
    assert entry["isDirectory"] is False
    assert entry["mime"] == "text/plain"
    assert entry["size"] == 5


def test_readdir_marks_directories_without_a_mime(auth):
    auth.post("/vfs/mkdir", json={"path": "home:/projects"})
    entry = next(e for e in readdir(auth) if e["filename"] == "projects")

    assert entry["isDirectory"] is True
    assert entry["mime"] is None


def test_writefile_reports_bytes_written(auth):
    assert upload(auth, "home:/a.txt", b"0123456789").get_json() == 10


def test_readfile_round_trips_content_and_mime(auth):
    upload(auth, "home:/page.html", b"<h1>hi</h1>")
    response = auth.get("/vfs/readfile", query_string={"path": "home:/page.html"})

    assert response.data == b"<h1>hi</h1>"
    assert response.mimetype == "text/html"


def test_readfile_honours_range_requests(auth):
    upload(auth, "home:/range.txt", b"0123456789")
    response = auth.get(
        "/vfs/readfile",
        query_string={"path": "home:/range.txt"},
        headers={"Range": "bytes=2-4"},
    )

    assert response.status_code == 206
    assert response.data == b"234"


def test_readfile_download_option_sets_attachment(auth):
    upload(auth, "home:/save.txt", b"x")
    options = json.dumps({"download": True})
    response = auth.get(
        "/vfs/readfile", query_string={"path": "home:/save.txt", "options": options}
    )

    assert "attachment" in response.headers["Content-Disposition"]


def test_exists_and_stat_track_the_filesystem(auth):
    assert auth.get("/vfs/exists", query_string={"path": "home:/gone.txt"}).get_json() is False

    upload(auth, "home:/gone.txt", b"x")

    assert auth.get("/vfs/exists", query_string={"path": "home:/gone.txt"}).get_json() is True
    assert auth.get("/vfs/stat", query_string={"path": "home:/gone.txt"}).get_json()["size"] == 1


def test_stat_of_a_missing_file_is_404(auth):
    assert auth.get("/vfs/stat", query_string={"path": "home:/nope"}).status_code == 404


def test_mkdir_rejects_an_existing_directory(auth):
    assert auth.post("/vfs/mkdir", json={"path": "home:/dup"}).get_json() is True
    assert auth.post("/vfs/mkdir", json={"path": "home:/dup"}).status_code == 409


def test_mkdir_ensure_tolerates_an_existing_directory(auth):
    auth.post("/vfs/mkdir", json={"path": "home:/dup"})
    response = auth.post("/vfs/mkdir", json={"path": "home:/dup", "options": {"ensure": True}})

    assert response.get_json() is True


def test_unlink_removes_directories_recursively(auth):
    auth.post("/vfs/mkdir", json={"path": "home:/tree"})
    upload(auth, "home:/tree/child.txt", b"x")

    assert auth.post("/vfs/unlink", json={"path": "home:/tree"}).get_json() is True
    assert [e["filename"] for e in readdir(auth)] == [".desktop"]


def test_touch_creates_an_empty_file(auth):
    assert auth.post("/vfs/touch", json={"path": "home:/empty"}).get_json() is True
    assert auth.get("/vfs/stat", query_string={"path": "home:/empty"}).get_json()["size"] == 0


def test_rename_moves_the_file(auth):
    upload(auth, "home:/old.txt", b"data")
    auth.post("/vfs/rename", json={"from": "home:/old.txt", "to": "home:/new.txt"})

    names = [e["filename"] for e in readdir(auth)]
    assert "new.txt" in names and "old.txt" not in names


def test_copy_leaves_the_source_in_place(auth):
    upload(auth, "home:/src.txt", b"data")
    auth.post("/vfs/copy", json={"from": "home:/src.txt", "to": "home:/dst.txt"})

    names = [e["filename"] for e in readdir(auth)]
    assert "src.txt" in names and "dst.txt" in names


def test_search_matches_nested_files_by_substring(auth):
    auth.post("/vfs/mkdir", json={"path": "home:/deep"})
    upload(auth, "home:/deep/report.txt", b"x")

    results = auth.post("/vfs/search", json={"root": "home:/", "pattern": "report"}).get_json()

    assert [r["path"] for r in results] == ["home:/deep/report.txt"]


def test_capabilities_reports_no_server_side_sort_or_pagination(auth):
    assert auth.get("/vfs/capabilities", query_string={"path": "home:/"}).get_json() == {
        "sort": False,
        "pagination": False,
    }


def test_unknown_vfs_method_is_404(auth):
    assert auth.post("/vfs/frobnicate", json={"path": "home:/"}).status_code == 404
