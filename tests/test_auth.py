def test_ping_needs_no_session(client):
    assert client.get("/ping").data == b"ok"


def test_login_rejects_wrong_password(client):
    response = client.post("/login", json={"username": "demo", "password": "wrong"})
    assert response.status_code == 403
    assert "error" in response.get_json()


def test_login_returns_profile_with_required_fields(client):
    profile = client.post("/login", json={"username": "demo", "password": "demo"}).get_json()
    assert profile["username"] == "demo"
    assert profile["id"]
    # `groups` carries the one role this server has. demo is in config.ADMINS,
    # which is what lets a demo found a permanent room at all.
    assert profile["groups"] == ["admin"]


def test_login_seeds_home_directory(auth):
    listing = auth.get("/vfs/readdir", query_string={"path": "home:/.desktop"}).get_json()
    assert [entry["filename"] for entry in listing] == [".shortcuts.json"]


def test_vfs_rejects_anonymous_requests(client):
    assert client.get("/vfs/readdir", query_string={"path": "home:/"}).status_code == 403


def test_logout_clears_the_session(auth):
    assert auth.post("/logout").get_json() == {}
    assert auth.get("/vfs/readdir", query_string={"path": "home:/"}).status_code == 403
