"""Flask application serving the OS.js client and its backend API.

Route map mirrors what @osjs/client requests: /ping, /login, /logout,
/settings and /vfs/<method>. Everything else is the built client in dist/.
"""

import atexit
import json
import logging
import uuid

from flask import (
    Flask,
    current_app,
    jsonify,
    request,
    send_file,
    send_from_directory,
    session,
)
from flask_sock import Sock

from . import bus as bus_module
from . import chat, config, sockets, timeline, vfs
from .vfs import VfsError

logger = logging.getLogger(__name__)

SETTINGS_PATH = "home:/.osjs/settings.json"

# Methods the client sends as GET with query parameters; the rest are JSON POSTs.
GET_METHODS = {"capabilities", "exists", "stat", "readdir", "readfile"}

# Mutations worth announcing on the system stream. The stream is the machine
# half of the chat design: a producer the server owns, arriving in the same
# window as a conversation.
ANNOUNCED_METHODS = {
    "writefile": "wrote",
    "mkdir": "created",
    "unlink": "deleted",
    "touch": "touched",
    "rename": "renamed",
    "copy": "copied",
}


def create_app():
    app = Flask(__name__, static_folder=str(config.DIST), static_url_path="")
    app.secret_key = config.SECRET_KEY
    app.config["SESSION_COOKIE_SAMESITE"] = "Lax"
    app.permanent_session_lifetime = config.SESSION_LIFETIME

    registry = sockets.Registry()
    app.extensions["sockets"] = registry
    app.extensions["chat"] = start_messaging(registry)

    register_routes(app)
    register_socket(app)
    return app


def start_messaging(registry):
    """Bring up the timeline, the bus and the Chat handler.

    The proxy is started here rather than by the Makefile so that `make serve`
    stays one command: the first process to come up claims it, and any further
    worker finds it claimed and simply connects.
    """
    timeline.init()

    worker = uuid.uuid4().hex[:8]
    timeline.clear_worker(worker)
    atexit.register(release_worker, worker)

    broker = bus_module.Broker()
    broker.start()
    bus = bus_module.Bus()
    service = chat.ChatService(bus, registry)
    bus.start(service.deliver)

    chat.ensure_system_stream()
    sockets.register_application_handler("Chat", service.handle)

    service.worker = worker
    service.broker = broker
    return service


def release_worker(worker):
    """Drop this worker's presence rows on the way out.

    Swallows everything: it runs at interpreter shutdown, where the store may
    already be gone and where an exception helps nobody.
    """
    try:
        timeline.clear_worker(worker)
    except Exception:  # pragma: no cover - shutdown path
        logger.debug("Could not clear presence for worker %s", worker, exc_info=True)


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
            written = vfs.writefile(username, request.form.get("path"), upload.stream)
            announce(method, username, request.form.get("path"))
            return jsonify(written)

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

        result = handler()
        announce(method, username, fields.get("path") or fields.get("to"))
        return jsonify(result)


def announce(method, username, path):
    """Report a filesystem change on the system stream.

    Only a mutation that succeeded gets here, and a failure to publish is
    swallowed: a stream is a convenience and must never be able to fail a
    request that has already been carried out.
    """
    verb = ANNOUNCED_METHODS.get(method)
    service = current_app.extensions.get("chat")
    if verb is not None and service is not None and path:
        chat.publish_system_event(service, f"{username} {verb} {path}")


def register_socket(app):
    """Mount the core websocket on `/`, where the client expects it.

    Werkzeug routes websocket and HTTP rules separately, so this shares the
    path with the index route above.
    """
    sock = Sock(app)
    registry = app.extensions["sockets"]
    service = app.extensions["chat"]
    max_age = int(config.SESSION_LIFETIME.total_seconds() * 1000)

    @sock.route("/")
    def core_socket(ws):
        user = current_user()
        if user is None:
            ws.close(1008, "Not authenticated")
            return

        # Presence is a row rather than a set in memory, so the roster is right
        # across workers. `sockets.serve` needs to know none of this: the
        # subscriptions are keyed by this websocket and released below.
        username = user.get("username")
        presence_id = timeline.arrive(username, service.worker)
        service.connect(ws)
        service.announce_presence(username, True)
        try:
            sockets.serve(registry, ws, user, config.WS_PING_INTERVAL, max_age)
        finally:
            service.disconnect(ws)
            timeline.depart(presence_id)
            service.announce_presence(username, False)


def main():
    logging.basicConfig(level=logging.INFO)

    if not (config.DIST / "index.html").is_file():
        raise SystemExit(f"No client build in {config.DIST}. Run 'make client' first.")

    config.VFS_ROOT.mkdir(parents=True, exist_ok=True)
    config.RUN_DIR.mkdir(parents=True, exist_ok=True)
    app = create_app()
    app.run(host=config.HOST, port=config.PORT)


if __name__ == "__main__":
    main()
