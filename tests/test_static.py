def test_index_is_served_at_the_root(client):
    assert b"minos" in client.get("/").data


def test_api_routes_are_not_shadowed_by_the_static_handler(client):
    assert client.get("/vfs/readdir", query_string={"path": "home:/"}).status_code == 403
