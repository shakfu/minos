# minos

A web desktop: a Python backend, and a TypeScript front end built on it.

It started as the [OS.js](https://www.os-js.org) v3 client against a Python
server that reimplements the contract OS.js expects. That client has since been
removed; `client/` is the replacement, written from scratch against the same
backend. The server still speaks the OS.js wire format -- route shapes, `osjs/*`
websocket message names, and the `osjs:` mountpoint are all kept as-is.

## Run

```
make client   # build the minos front end -> dist/index.html
make serve    # Flask on http://127.0.0.1:8000
```

Then http://127.0.0.1:8000/, logging in as `demo` / `demo`.

```
make test     # typecheck, vitest, pytest
make dev      # Vite with hot reload, proxying the API to a running `make serve`
```

`make dev` opens a browser on http://localhost:5173/. `BROWSER=none make dev`
starts the server without one.

`serve`, `dev` and `test` install what they need first. That needs
[uv](https://docs.astral.sh/uv/) for the Python venv and npm for the client;
where the node install ships without npm, the Makefile falls back to corepack's.

## Layout

| Path | Contents |
|-|-|
| `client/` | The minos front end. TypeScript, Vite, no UI framework. |
| `server/` | Flask app, VFS, websocket, config. |
| `tests/` | pytest suite against the Flask test client. |
| `dist/` | Build output. Generated. |
| `vfs/` | User home directories. Generated. |

## Front end

`client/` is vanilla TypeScript. A window manager is imperative DOM work --
drag, resize, stacking, focus -- so there is no virtual DOM to fight; the whole
toolchain is Vite and TypeScript.

| Path | Contents |
|-|-|
| `client/src/core/` | API client, session, websocket, path helpers, event bus. |
| `client/src/wm/` | Window and WindowManager. |
| `client/src/ui/` | Panel, menu, dialogs, login. |
| `client/src/apps/` | File manager and file viewer. |

Two things worth knowing:

- `core/api.ts` is the only module that knows the wire format. The tests in
  `client/tests/api.test.ts` pin every URL and body shape, so a drift from the
  frozen server contract fails there rather than in the browser.
- `Session.patchDesktop` merges into a `minos/desktop` key and writes the whole
  settings object back rather than overwriting it. Homes created under the old
  client still carry `osjs/*` keys, and a blind overwrite would drop them.

`WindowManager` takes its workspace rect as an injected function rather than
measuring the DOM, which is what makes the geometry testable without layout.

## API

The client talks to these routes. Shapes still match `@osjs/server`: the
contract is frozen, and `core/api.ts` is pinned to it by its tests.

| Route | Purpose |
|-|-|
| `GET /ping` | Session keepalive. |
| `POST /login` | Returns a user profile, sets the session cookie. |
| `POST /logout` | Clears the session. |
| `GET,POST /settings` | Per-user settings, stored at `home:/.osjs/settings.json`. |
| `GET /vfs/{capabilities,exists,stat,readdir,readfile}` | Query-parameter reads. |
| `POST /vfs/{writefile,mkdir,unlink,touch,rename,copy,search}` | Writes; `writefile` is multipart. |
| `/*` | Static files from `dist/`. |
| `WS /` | Core websocket. Shares the path with the index route. |

Paths are `<mountpoint>:/<path>`. Two mountpoints are configured: `osjs:/` maps
to `dist/` read-only, `home:/` to `vfs/<username>/`. Every resolved path is
checked against its mountpoint root, so traversal and symlinks cannot escape it.

## WebSocket

One socket carries every named message, as JSON `{name, params}` frames. It
requires a session; an anonymous upgrade is closed with 1008.

Server to client:

- `osjs/core:connected` on connect, carrying the session lifetime.
- `osjs/core:ping` after 30 seconds of client silence.

Nothing else is pushed. `Registry.broadcast` and `broadcast_to_user` are there
for handlers to use, but no server-side code calls them. Hot reload during
development is Vite's, through `make dev`.

Client to server: only `osjs/application:socket:message` is accepted. Every
other `osjs*` name is refused, so a page cannot forge core events. A frame
carries `{pid, name, args}`, and `name` selects the handler:

```python
from server.sockets import register_application_handler

register_application_handler("Echo", lambda conn, respond, args: respond(*args))
```

`respond` answers the one connection, quoting the `pid` back so the caller can
match the reply to its request.

## Scope

This is a demo, not a deployment.

- Credentials are a plaintext dict in `server/config.py`; the session key
  defaults to a fixed string. Both need replacing before the server is exposed.
- The Flask development server handles the websocket. It is threaded, which is
  enough for a demo; anything real wants gunicorn with a gevent worker.
- No VFS change notifications. `osjs/vfs:watch:change` has no consumer in the
  client, so nothing watches the user filesystem.
- No server-side application handlers ship with the project. `register_application_handler`
  is there, but nothing calls it and the client sends no application messages.
- Apps are compiled into the one bundle and registered statically in
  `client/src/apps/index.ts`. Nothing is loaded at runtime.
