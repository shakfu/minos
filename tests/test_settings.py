def test_settings_default_to_an_empty_object(auth):
    assert auth.get("/settings").get_json() == {}


def test_settings_round_trip(auth):
    payload = {"osjs/desktop": {"theme": "StandardTheme"}}

    assert auth.post("/settings", json=payload).get_json() is True
    assert auth.get("/settings").get_json() == payload


def test_settings_require_a_session(client):
    assert client.get("/settings").status_code == 403
    assert client.post("/settings", json={}).status_code == 403
