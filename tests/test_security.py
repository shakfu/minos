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


def test_uploaded_html_is_not_rendered_inline(auth):
    """A document served inline from this origin can script it.

    It would reach the whole /vfs API with the viewer's cookie, so anything not
    known-safe to render is handed over as a download instead.
    """
    upload(auth, "home:/x.html", b"<script>alert(document.cookie)</script>")
    response = auth.get("/vfs/readfile", query_string={"path": "home:/x.html"})

    assert "attachment" in response.headers["Content-Disposition"]
    # The mime is still reported as the frozen contract requires.
    assert response.mimetype == "text/html"


def test_svg_is_not_rendered_inline(auth):
    """An image that carries script is not a safe inline type."""
    upload(auth, "home:/x.svg", b"<svg xmlns='http://www.w3.org/2000/svg'></svg>")
    response = auth.get("/vfs/readfile", query_string={"path": "home:/x.svg"})

    assert "attachment" in response.headers["Content-Disposition"]


@pytest.mark.parametrize(
    ("name", "body", "mime"),
    [("p.png", b"\x89PNG\r\n\x1a\n", "image/png"), ("n.txt", b"hello", "text/plain")],
)
def test_previewable_types_are_still_inline(auth, name, body, mime):
    """The viewer renders these, so the disposition must not change for them."""
    upload(auth, f"home:/{name}", body)
    response = auth.get("/vfs/readfile", query_string={"path": f"home:/{name}"})

    assert response.mimetype == mime
    assert "attachment" not in response.headers.get("Content-Disposition", "")


def test_security_headers_are_present_on_every_response(auth):
    for path in ("/", "/ping"):
        headers = auth.get(path).headers
        assert headers["X-Content-Type-Options"] == "nosniff"
        assert headers["X-Frame-Options"] == "DENY"
        assert "default-src 'self'" in headers["Content-Security-Policy"]


def test_the_policy_allows_the_websocket_back_to_this_host(auth):
    """connect-src has to name ws:// explicitly: the page is http://."""
    policy = auth.get("/").headers["Content-Security-Policy"]

    assert "connect-src 'self' ws://localhost wss://localhost" in policy
    assert "script-src" not in policy  # default-src covers it, without unsafe-inline


def test_settings_reject_a_payload_that_is_not_an_object(auth):
    """patchDesktop merges so a client cannot drop another's keys.

    A payload of another shape would destroy them, so it never reaches the file.
    """
    assert auth.post("/settings", json={"osjs/desktop": {"theme": "x"}}).get_json() is True

    assert auth.post("/settings", json=[1, 2, 3]).status_code == 400
    assert auth.post("/settings", json="nope").status_code == 400

    # The good settings survived the rejected writes.
    assert auth.get("/settings").get_json() == {"osjs/desktop": {"theme": "x"}}
