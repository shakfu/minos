"""Flask application serving the OS.js client and its backend API.

Route map mirrors what @osjs/client requests: /ping, /login, /logout,
/settings and /vfs/<method>. Everything else is the built client in dist/.
"""

import json
import logging

from flask import Flask, jsonify, request, send_file, send_from_directory, session
from flask_sock import Sock

from . import config, sockets, vfs
from .vfs import VfsError

logger = logging.getLogger(__name__)

SETTINGS_PATH = "home:/.osjs/settings.json"

# Methods the client sends as GET with query parameters; the rest are JSON POSTs.
GET_METHODS = {"capabilities", "exists", "stat", "readdir", "readfile"}


def create_app():
    app = Flask(__name__, static_folder=str(config.DIST), static_url_path="")
    app.secret_key = config.SECRET_KEY
    app.config["SESSION_COOKIE_SAMESITE"] = "Lax"
    app.permanent_session_lifetime = config.SESSION_LIFETIME

    app.extensions["sockets"] = sockets.Registry()

    register_routes(app)
    register_socket(app)
    return app


def current_user():
    """Return the logged-in profile, or None when the session is anonymous."""
    return session.get("user")


def require_user():
    user = current_user()
    if user is None:
        raise VfsError("Not authenticated", 403)
    return user


def parse_options(raw):
    """Decode the `options` field, which arrives as JSON text on GET requests."""
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str):
        try:
            parsed = json.loads(raw)
        except ValueError:
            return {}
        return parsed if isinstance(parsed, dict) else {}
    return {}


def register_routes(app):
    @app.errorhandler(VfsError)
    def on_vfs_error(error):
        return jsonify(error=str(error)), error.status

    @app.route("/")
    def index():
        return send_from_directory(config.DIST, "index.html")

    @app.route("/ping")
    def ping():
        return "ok"

    @app.route("/login", methods=["POST"])
    def login():
        payload = request.get_json(silent=True) or {}
        username = payload.get("username")
        password = payload.get("password")

        if username not in config.USERS or config.USERS[username] != password:
            return jsonify(error="Invalid login or permission denied"), 403

        profile = {
            "id": username,
            "username": username,
            "name": username,
            "groups": [],
        }
        vfs.ensure_home(username)
        session.permanent = True
        session["user"] = profile
        return jsonify(profile)

    @app.route("/logout", methods=["POST"])
    def logout():
        session.clear()
        return jsonify({})

    @app.route("/settings", methods=["GET", "POST"])
    def settings():
        user = require_user()
        target = vfs.resolve(SETTINGS_PATH, user["username"], "writefile")

        if request.method == "GET":
            try:
                return jsonify(json.loads(target.read_text()))
            except (OSError, ValueError):
                return jsonify({})

        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(request.get_json(silent=True) or {}))
        return jsonify(True)

    @app.route("/vfs/<method>", methods=["GET", "POST"])
    def vfs_request(method):
        user = require_user()
        username = user["username"]

        if method == "writefile":
            upload = request.files.get("upload")
            if upload is None:
                raise VfsError("Missing upload field")
            return jsonify(vfs.writefile(username, request.form.get("path"), upload.stream))

        fields = request.args if method in GET_METHODS else (request.get_json(silent=True) or {})
        options = parse_options(fields.get("options"))

        if method == "readfile":
            target = vfs.readfile(username, fields.get("path"), options)
            return send_file(
                target,
                mimetype=vfs.guess_mime(target),
                conditional=True,
                as_attachment=bool(options.get("download")),
                download_name=target.name,
            )

        handlers = {
            "capabilities": lambda: vfs.capabilities(username, fields.get("path"), options),
            "exists": lambda: vfs.exists(username, fields.get("path"), options),
            "stat": lambda: vfs.stat(username, fields.get("path"), options),
            "readdir": lambda: vfs.readdir(username, fields.get("path"), options),
            "mkdir": lambda: vfs.mkdir(username, fields.get("path"), options),
            "unlink": lambda: vfs.unlink(username, fields.get("path"), options),
            "touch": lambda: vfs.touch(username, fields.get("path"), options),
            "copy": lambda: vfs.copy(username, fields.get("from"), fields.get("to"), options),
            "rename": lambda: vfs.rename(username, fields.get("from"), fields.get("to"), options),
            "search": lambda: vfs.search(
                username, fields.get("root"), fields.get("pattern") or "", options
            ),
        }

        handler = handlers.get(method)
        if handler is None:
            raise VfsError(f"No such VFS method: {method}", 404)

        return jsonify(handler())


def register_socket(app):
    """Mount the core websocket on `/`, where the client expects it.

    Werkzeug routes websocket and HTTP rules separately, so this shares the
    path with the index route above.
    """
    sock = Sock(app)
    registry = app.extensions["sockets"]
    max_age = int(config.SESSION_LIFETIME.total_seconds() * 1000)

    @sock.route("/")
    def core_socket(ws):
        user = current_user()
        if user is None:
            ws.close(1008, "Not authenticated")
            return

        sockets.serve(registry, ws, user, config.WS_PING_INTERVAL, max_age)


def main():
    logging.basicConfig(level=logging.INFO)

    if not (config.DIST / "index.html").is_file():
        raise SystemExit(f"No client build in {config.DIST}. Run 'make client' first.")

    config.VFS_ROOT.mkdir(parents=True, exist_ok=True)
    app = create_app()
    app.run(host=config.HOST, port=config.PORT)


if __name__ == "__main__":
    main()
