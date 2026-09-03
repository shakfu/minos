# minos

A web desktop: a Python backend, and a TypeScript front end built on it.

It started as the [OS.js](https://www.os-js.org) v3 client against a Python server that reimplements the contract OS.js expects. That client has since been removed; `client/` is the replacement, written from scratch against the same backend. The server still speaks the OS.js wire format -- route shapes, `osjs/*` websocket message names, and the `osjs:` mountpoint are all kept as-is.

## Run

```
make client   # build the minos front end -> dist/index.html
make serve    # Flask on http://127.0.0.1:8000
```

Then http://127.0.0.1:8000/, logging in as `demo` / `demo`. There are also `alice` and `bob`, with passwords to match; the chat and stream windows need two of them to show anything, so open a second browser profile and log in as another.

```
make test     # typecheck, vitest, pytest
make dev      # Vite with hot reload, proxying the API to a running `make serve`
```

`make dev` opens a browser on http://localhost:5173/. `BROWSER=none make dev` starts the server without one.

`serve`, `dev` and `test` install what they need first. That needs [uv](https://docs.astral.sh/uv/) for the Python venv and npm for the client; where the node install ships without npm, the Makefile falls back to corepack's.

## Layout

| Path | Contents |
|-|-|
| `client/` | The minos front end. TypeScript, Vite, no UI framework. |
| `server/` | Flask app, VFS, websocket, message bus, timeline, config. |
| `tests/` | pytest suite against the Flask test client. |
| `dist/` | Build output. Generated. |
| `vfs/` | User home directories. Generated. |
| `.run/` | Timeline database and the bus sockets. Generated. |

## Front end

`client/` is vanilla TypeScript. A window manager is imperative DOM work -- drag, resize, stacking, focus -- so there is no virtual DOM to fight; the whole toolchain is Vite and TypeScript.

| Path | Contents |
|-|-|
| `client/src/core/` | API client, session, websocket, chat protocol, path helpers, event bus. |
| `client/src/wm/` | Window and WindowManager. |
| `client/src/ui/` | Panel, menu, dialogs, login. |
| `client/src/apps/` | File manager, file viewer, chat and streams. |

Two things worth knowing:

- `core/api.ts` is the only module that knows the wire format. The tests in `client/tests/api.test.ts` pin every URL and body shape, so a drift from the frozen server contract fails there rather than in the browser.

- `Session.patchDesktop` merges into a `minos/desktop` key and writes the whole settings object back rather than overwriting it. Homes created under the old client still carry `osjs/*` keys, and a blind overwrite would drop them.

`WindowManager` takes its workspace rect as an injected function rather than measuring the DOM, which is what makes the geometry testable without layout.

## API

The client talks to these routes. Shapes still match `@osjs/server`: the contract is frozen, and `core/api.ts` is pinned to it by its tests.

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

Paths are `<mountpoint>:/<path>`. Two mountpoints are configured: `osjs:/` maps to `dist/` read-only, `home:/` to `vfs/<username>/`. Every resolved path is checked against its mountpoint root, so traversal and symlinks cannot escape it.

## WebSocket

One socket carries every named message, as JSON `{name, params}` frames. It requires a session; an anonymous upgrade is closed with 1008.

Server to client:

- `osjs/core:connected` on connect, carrying the session lifetime.

- `osjs/core:ping` after 30 seconds of client silence.

- `osjs/application:socket:message` for chat traffic, both replies and unsolicited pushes. `Registry.broadcast` fans these out; a frame whose `pid` is null is a push rather than an answer.

Hot reload during development is Vite's, through `make dev`.

Client to server: only `osjs/application:socket:message` is accepted. Every other `osjs*` name is refused, so a page cannot forge core events. A frame carries `{pid, name, args}`, and `name` selects the handler:

```python
from server.sockets import register_application_handler

register_application_handler("Echo", lambda conn, respond, args: respond(*args))
```

`respond` answers the one connection, quoting the `pid` back so the caller can match the reply to its request. `server/chat.py` is the one handler that ships; the section below is what it does.

## Chat and streams

A window manager earns its keep when several things have to be visible at once. Chat is the case where that is obvious: a tab shows one conversation at a time and makes you remember the rest, while windows let you watch four and see which one moved.

So a room here is not a channel someone joined and named. It is **a set of people**, and the window is the view of that set:

- Open a person and you have a one-to-one.
- Drag a second person onto the window and it is a group. Nothing was created, nothing was named -- the membership grew.
- Drop one room window onto another and the two memberships merge into one conversation.

Machine streams are the same object with a producer instead of a person. The `system` stream carries what the server does -- every VFS write, mkdir and rename shows up there -- in a window that behaves like any other, minus the composer. Nothing in the client knows the difference; only `kind` differs.

### How a message travels

```
send ─┬─> timeline.append ....... assigns the room's next sequence (SQLite)
      │
      └─> bus.publish ──> XSUB ─ zmq.proxy ─ XPUB ──> relay ──> Registry.broadcast ──> websockets
                                     │                  │
                            one owning process    one per worker
```

| Module | Responsibility |
|-|-|
| `server/timeline.py` | The source of truth. Rooms, members, messages, presence, in SQLite. |
| `server/bus.py` | The ZeroMQ leg: an XSUB/XPUB forwarder, and a per-process publisher and relay. |
| `server/chat.py` | The application handler. `sync`, `history`, `send`, `open`, `invite`, `leave`, `merge`. |
| `client/src/core/chat.ts` | The protocol client, including gap repair. |
| `client/src/apps/Chat.ts` | The Dock, the room window, and the drag targets. |

The handler never touches the connection registry. It appends to the timeline, publishes, and returns; the relay in each worker decides who is locally connected and delivers. That is what makes a second worker work at all, and it is why `register_application_handler` did not need a registry argument.

The first process to start claims the forwarder by taking an exclusive `flock` on `.run/broker.lock` and runs `zmq.proxy` in a thread; the rest connect to it. A lock rather than a bind attempt, so a `kill -9` cannot lock everyone out behind a stale `ipc://` file. `python -m server.bus` runs it standalone if you would rather it not live inside a worker.

### Why sequence numbers

PUB/SUB drops rather than queues. A subscription that has not propagated yet, a high-water mark, a client offline for an hour -- all three lose messages, and none of them report it.

Every message therefore gets a per-room sequence number, assigned inside a `BEGIN IMMEDIATE` transaction so concurrent writers in different processes cannot collide. The client keeps a cursor per room and compares:

- at or below the cursor: seen already, drop it
- exactly one above: the next message, render it
- higher: something never arrived -- ask for everything past the cursor, and render that

One mechanism covers a dropped frame, a slow joiner and a reconnect, and it is why the client can treat the bus as unreliable without any of it showing. `client/tests/chat.test.ts` and `tests/test_bus.py` are mostly about these edges: a burst must start one backfill rather than one per message, and a subscription to `room.1` must not deliver `room.11`.

## Scope

This is a demo, not a deployment.

- Credentials are a plaintext dict in `server/config.py`; the session key defaults to a fixed string. Both need replacing before the server is exposed.

- The Flask development server handles the websocket. It is threaded, which is enough for a demo; anything real wants gunicorn with a gevent worker.

- VFS changes are announced onto the `system` stream, but only the ones a request made. Nothing watches the filesystem itself, and `osjs/vfs:watch:change` still has no consumer.

- Presence is a row per connection rather than a heartbeat. A worker killed outright leaves its rows behind until it starts again under the same id and clears them.

- The bus is not authenticated. Anything that can reach the `ipc://` sockets in `.run/` can publish to any room, so a multi-host deployment wants `tcp://` with CURVE rather than the defaults.

- Apps are compiled into the one bundle and registered statically in `client/src/apps/index.ts`. Nothing is loaded at runtime.
