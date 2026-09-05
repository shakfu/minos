"""The HTTP half of the contract: routes, sessions, settings and the VFS.

This half is frozen -- it is what @osjs/client asks for -- so these tests are
as much a record of what may not change as a check on what does.
"""

import json

import pytest

CSP_DIRECTIVES = [
    "default-src 'self'",
    "img-src 'self' data: blob:",
    "object-src 'none'",
    "base-uri 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
]

OK = 200
BAD_REQUEST = 400
FORBIDDEN = 403
NOT_FOUND = 404
CONFLICT = 409


# -- the session ------------------------------------------------------------


def test_ping_answers_without_a_session(anonymous):
    response = anonymous.ping()
    assert response.status == OK
    assert response.text == "ok"


def test_login_returns_the_frozen_profile_shape(anonymous):
    response = anonymous.login("alice", "alice")
    assert response.status == OK
    assert response.json == {
        "id": "alice",
        "username": "alice",
        "name": "alice",
        "groups": [],
    }


def test_an_administrator_is_named_on_the_profile(anonymous):
    """`groups` is the only channel the role travels on."""
    assert anonymous.login("demo", "demo").json["groups"] == ["admin"]


def test_a_wrong_password_is_refused_without_saying_which_half(anonymous):
    response = anonymous.login("alice", "bob")
    assert response.status == FORBIDDEN
    assert response.error == "Invalid login or permission denied"


def test_an_unknown_user_gets_the_same_answer(anonymous):
    response = anonymous.login("nobody", "nobody")
    assert response.status == FORBIDDEN
    assert response.error == "Invalid login or permission denied"


def test_logout_ends_the_session(session):
    http = session("alice")
    assert http.logout().json == {}
    assert http.settings().status == FORBIDDEN


@pytest.mark.parametrize(
    "method,path",
    [("GET", "/settings"), ("POST", "/settings"), ("GET", "/vfs/readdir")],
)
def test_every_other_route_needs_a_session(anonymous, method, path):
    response = anonymous.call(method, path)
    assert response.status == FORBIDDEN
    assert response.error == "Not authenticated"


# -- headers ----------------------------------------------------------------


def test_security_headers_are_on_every_response(anonymous, server):
    headers = anonymous.ping().headers
    assert headers["X-Content-Type-Options"] == "nosniff"
    assert headers["X-Frame-Options"] == "DENY"

    policy = headers["Content-Security-Policy"]
    for directive in CSP_DIRECTIVES:
        assert directive in policy

    # The socket is ws:// while the page is http://, so 'self' does not cover
    # it and the host has to be named.
    host = server.base.removeprefix("http://")
    assert f"connect-src 'self' ws://{host} wss://{host}" in policy


# -- settings ---------------------------------------------------------------


def test_settings_start_empty_and_round_trip(session):
    http = session("bob")
    assert http.settings().json == {}

    assert http.put_settings({"osjs/desktop": {"theme": "dark"}}).json is True
    assert http.settings().json == {"osjs/desktop": {"theme": "dark"}}


def test_settings_are_replaced_wholesale(session):
    """A client merges; the server stores what it is given."""
    http = session("bob")
    http.put_settings({"a": 1, "b": 2})
    http.put_settings({"a": 9})
    assert http.settings().json == {"a": 9}


def test_settings_refuse_anything_but_an_object(session):
    response = session("bob").call("POST", "/settings", [1, 2])
    assert response.status == BAD_REQUEST
    assert response.error == "Settings must be a JSON object"


def test_settings_are_per_user(session):
    session("alice").put_settings({"who": "alice"})
    session("bob").put_settings({"who": "bob"})
    assert session("alice").settings().json == {"who": "alice"}


# -- the VFS ----------------------------------------------------------------


def test_capabilities_reports_the_frozen_answer(session):
    response = session("alice").vfs("capabilities", path="home:/")
    assert response.json == {"sort": False, "pagination": False}


def test_a_write_read_and_delete_round_trip(session, unique):
    http = session("alice")
    path = f"home:/{unique('file')}.txt"

    assert http.upload(path, b"hello").json == 5
    assert http.vfs("exists", path=path).json is True
    assert http.call("GET", "/vfs/readfile", params={"path": path}).body == b"hello"
    assert http.vfs("unlink", path=path).json is True
    assert http.vfs("exists", path=path).json is False


def test_stat_returns_a_descriptor(session, unique):
    http = session("alice")
    path = f"home:/{unique('note')}.txt"
    http.upload(path, b"abc")

    descriptor = http.vfs("stat", path=path).json
    assert descriptor["isFile"] is True
    assert descriptor["isDirectory"] is False
    assert descriptor["mime"] == "text/plain"
    assert descriptor["size"] == 3
    assert descriptor["path"] == path
    assert descriptor["filename"] == path.rsplit("/", 1)[-1]
    assert set(descriptor["stat"]) == {
        "size", "mode", "atime", "mtime", "ctime", "atimeMs", "mtimeMs", "ctimeMs",
    }
    assert descriptor["stat"]["mtime"].endswith("+00:00")


def test_stat_of_a_missing_file_is_not_found(session):
    response = session("alice").vfs("stat", path="home:/absent")
    assert response.status == NOT_FOUND


def test_readdir_lists_a_directory_with_full_virtual_paths(session, unique):
    http = session("alice")
    directory = unique("dir")
    http.vfs("mkdir", path=f"home:/{directory}")
    http.upload(f"home:/{directory}/one.txt", b"1")

    entries = http.vfs("readdir", path=f"home:/{directory}").json
    assert [entry["path"] for entry in entries] == [f"home:/{directory}/one.txt"]


def test_readdir_sorts_by_lowercased_name(session, unique):
    http = session("alice")
    directory = unique("sorted")
    http.vfs("mkdir", path=f"home:/{directory}")
    for name in ("b.txt", "A.txt", "c.txt"):
        http.upload(f"home:/{directory}/{name}", b"x")

    entries = http.vfs("readdir", path=f"home:/{directory}").json
    assert [entry["filename"] for entry in entries] == ["A.txt", "b.txt", "c.txt"]


def test_a_directory_has_no_mime(session, unique):
    http = session("alice")
    directory = unique("dir")
    http.vfs("mkdir", path=f"home:/{directory}")
    assert http.vfs("stat", path=f"home:/{directory}").json["mime"] is None


def test_mkdir_refuses_a_collision_unless_ensure_is_set(session, unique):
    http = session("alice")
    path = f"home:/{unique('dir')}"
    assert http.vfs("mkdir", path=path).json is True

    assert http.vfs("mkdir", path=path).status == CONFLICT
    assert http.vfs("mkdir", path=path, options={"ensure": True}).json is True


def test_copy_and_rename(session, unique):
    http = session("alice")
    stem = unique("move")
    http.upload(f"home:/{stem}-a.txt", b"data")

    assert http.vfs("copy", **{"from": f"home:/{stem}-a.txt", "to": f"home:/{stem}-b.txt"}).json is True
    assert http.vfs("rename", **{"from": f"home:/{stem}-b.txt", "to": f"home:/{stem}-c.txt"}).json is True
    assert http.vfs("exists", path=f"home:/{stem}-b.txt").json is False
    assert http.call("GET", "/vfs/readfile", params={"path": f"home:/{stem}-c.txt"}).body == b"data"


def test_search_wraps_a_bare_pattern_in_wildcards(session, unique):
    http = session("alice")
    directory = unique("search")
    http.vfs("mkdir", path=f"home:/{directory}")
    http.upload(f"home:/{directory}/report-final.txt", b"x")
    http.upload(f"home:/{directory}/other.txt", b"x")

    found = http.vfs("search", root=f"home:/{directory}", pattern="report")
    assert [entry["filename"] for entry in found.json] == ["report-final.txt"]


def test_search_is_case_insensitive(session, unique):
    http = session("alice")
    directory = unique("search")
    http.vfs("mkdir", path=f"home:/{directory}")
    http.upload(f"home:/{directory}/README.md", b"x")

    found = http.vfs("search", root=f"home:/{directory}", pattern="readme")
    assert [entry["filename"] for entry in found.json] == ["README.md"]


def test_an_unparseable_options_field_means_no_options(session, unique):
    """It arrives as JSON text on a GET, and a bad one is not worth an error."""
    http = session("alice")
    path = f"home:/{unique('dir')}"
    response = http.call("GET", "/vfs/exists", params={"path": path, "options": "{oops"})
    assert response.status == OK


def test_an_unknown_vfs_method_is_not_found(session):
    response = session("alice").vfs("frobnicate", path="home:/")
    assert response.status == NOT_FOUND
    assert response.error == "No such VFS method: frobnicate"


# -- mountpoints ------------------------------------------------------------


def test_a_path_may_not_escape_its_mountpoint(session):
    response = session("alice").vfs("readdir", path="home:/../../etc")
    assert response.status == FORBIDDEN
    assert response.error == "Path escapes its mountpoint"


def test_the_build_mountpoint_is_read_only(session):
    response = session("alice").vfs("mkdir", path="osjs:/anything")
    assert response.status == FORBIDDEN
    assert "read-only" in response.error


def test_the_build_mountpoint_is_readable(session):
    assert session("alice").vfs("exists", path="osjs:/index.html").json is True


def test_an_unknown_mountpoint_is_not_found(session):
    response = session("alice").vfs("readdir", path="nowhere:/")
    assert response.status == NOT_FOUND
    assert response.error == "No such mountpoint: nowhere"


def test_a_malformed_path_is_a_bad_request(session):
    response = session("alice").vfs("readdir", path="no-colon")
    assert response.status == BAD_REQUEST


def test_homes_are_separate(session, unique):
    """alice's home is not bob's, and neither can name the other's."""
    name = unique("private")
    session("alice").upload(f"home:/{name}.txt", b"secret")
    assert session("bob").vfs("exists", path=f"home:/{name}.txt").json is False


# -- disposition ------------------------------------------------------------


@pytest.mark.parametrize(
    "name,contents,inline",
    [
        ("plain.txt", b"text", True),
        ("pixel.png", b"\x89PNG\r\n\x1a\n", True),
        ("page.html", b"<b>x</b>", False),
        ("vector.svg", b"<svg/>", False),
    ],
)
def test_only_types_that_cannot_script_are_served_inline(
    session, unique, name, contents, inline
):
    """SVG is excluded deliberately: an image that carries script."""
    http = session("alice")
    path = f"home:/{unique('disp')}-{name}"
    http.upload(path, contents)

    response = http.call("GET", "/vfs/readfile", params={"path": path})
    disposition = response.headers.get("Content-Disposition", "")
    assert ("attachment" in disposition) is not inline


def test_the_download_option_forces_an_attachment(session, unique):
    http = session("alice")
    path = f"home:/{unique('disp')}.txt"
    http.upload(path, b"text")

    response = http.call(
        "GET",
        "/vfs/readfile",
        params={"path": path, "options": json.dumps({"download": True})},
    )
    assert "attachment" in response.headers["Content-Disposition"]


def test_the_mime_is_reported_whatever_the_disposition(session, unique):
    http = session("alice")
    path = f"home:/{unique('disp')}.svg"
    http.upload(path, b"<svg/>")

    response = http.call("GET", "/vfs/readfile", params={"path": path})
    assert response.headers["Content-Type"].startswith("image/svg+xml")


# -- the index ----------------------------------------------------------------


def test_the_index_is_served_from_the_build_directory(anonymous):
    response = anonymous.call("GET", "/")
    assert response.status == OK
    assert b"minos" in response.body
