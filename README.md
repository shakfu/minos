# minos

A conversation server in Go, a terminal client, and a frozen wire contract between them.

There are two servers and that is deliberate. `go/` is the implementation. `server/` and `messaging/` are the specification it was written from -- executable, readable, and not meant to be deployed. Neither is authoritative on its own: [docs/wire-contract.md](docs/wire-contract.md) is, and `tests/conformance/` holds both to it.

It started as the [OS.js](https://www.os-js.org) v3 client against a Python server that reimplements the contract OS.js expects. Two front ends have been removed since: the OS.js client, and the web desktop written to replace it. `tui/` is the current one, and the desktop metaphor it dropped took the old conversation model with it. The server still speaks the OS.js wire format -- route shapes, `osjs/*` websocket message names, and the `osjs:` mountpoint are all kept as-is, which is what lets a front end be replaced without touching the server.

What a room, a group and a channel actually are is settled in [chat-concepts.md](chat-concepts.md), before and independently of any way of reaching them. Read that first if you are changing behaviour rather than code; the short version is that **a room is a place** -- it has its own identity, two rooms may hold the same people, and who may enter is a grant naming a user or a whole group.

## Run

```
make serve-go # the Go server on http://127.0.0.1:8000
make tui      # the terminal client, in another shell
```

`make serve` runs the Python one instead, on the same port and the same contract. Use it to read what a behaviour is supposed to be; use `serve-go` for anything else. Go 1.25 or newer builds it.

Log in as `demo` / `demo`. There are also `alice` and `bob`, with passwords to match; a conversation needs two of them, so run `make tui` again in a third shell and log in as another. `demo` is the only administrator, which is what lets it found a permanent room or manage a group.

Neither server needs a browser build. Both warn and serve the API alone if `dist/` is empty, because the terminal client needs the routes and the websocket rather than a bundle.

Inside the client, `/help` lists the commands. `/open alice` raises a room, `/meet alice` raises one that is discarded when everyone leaves, `/create Engineering` founds a permanent one, and `/invite @Team` admits a whole group.

```
make test          # typecheck, vitest, pytest
make conformance-go  # the wire contract, against the Go server
make conformance     # the same suite, against the Python one
```

`tests/conformance/` talks to a server over HTTP and a websocket and imports none
of its code, which is what lets one suite hold two implementations to one
contract. `MINOS_CONFORMANCE_CMD` points it at any server and
`MINOS_CONFORMANCE_URL` at one already running.

`serve`, `tui` and `test` install what they need first, which needs [uv](https://docs.astral.sh/uv/) for the Python venv. There is no JavaScript toolchain any more.

Python dependencies live in `pyproject.toml`: the four the server and terminal client run on, and a `dev` dependency group for the test tooling. A deployment installs `uv pip install -r pyproject.toml` and gets no test tooling; `make test` adds `--group dev`.

## Layout

| Path | Contents |
|-|-|
| `chat-concepts.md` | The model: what a room, group and channel are. Front-end independent. |
| `docs/` | The wire contract, and development notes under `docs/dev/`. |
| `go/` | The server. One process, a goroutine per connection, SQLite. |
| `tui/` | The terminal client. Python, curses, no UI framework. |
| `server/` | The specification: Flask app, VFS, websocket, config, and the adapter binding the two below. |
| `messaging/` | The specification's conversation half: timeline, ZeroMQ bus, operations. |
| `tests/` | pytest. Most of it drives the Flask test client; the messaging tests need no server. |
| `tests/conformance/` | The wire contract as a black-box suite. Imports no implementation. |
| `dist/` | What the `osjs:` mountpoint serves. Optional, and nothing in the tree builds it. |
| `vfs/` | User home directories. Generated. |
| `.run/` | Timeline database, bus sockets, and the liveness locks. Generated. |
| `TODO.md` | Known work not done, including the parked rewrite in a compiled language. |
| `pyproject.toml` | Python dependencies and pytest configuration. |

## The server

`go/` is one process. A goroutine per connection, an in-process fan-out, and
SQLite for the timeline. The message bus, the worker leases and the liveness
locks in `messaging/` have no counterpart here: they existed because CPython
needed several worker processes, and one process needs none of it.

| Path | Contents |
|-|-|
| `go/cmd/minosd` | The entry point: configuration, start-up, shutdown. |
| `go/internal/httpapi` | Routes, the signed session cookie, and the websocket upgrade. |
| `go/internal/socket` | The frame format, the connection registry, and the fan-out. |
| `go/internal/chat` | The seam: operation names to messaging calls, occupancy per connection. |
| `go/internal/messaging` | The operations. Knows nothing about how a caller is connected. |
| `go/internal/timeline` | The store: rooms, grants, messages, and the per-room sequence. |
| `go/internal/vfs` | Mountpoints, path resolution, and file operations. |

Two things it does that the specification does not, both found by porting:

- **A send is a queue push.** Each connection has an outbound queue and one
  writer goroutine, so a fan-out is never held up by the slowest recipient, and
  a client that falls too far behind is disconnected rather than waited for.
- **A websocket outlives the request that opened it.** Its lifetime is the
  server's, not `r.Context()`, which `net/http` may cancel once a connection is
  hijacked.

## Front end

`tui/` is the terminal client, in Python with nothing under it but `curses`, `urllib` and the websocket client `flask-sock` already depends on. It needs no browser and no build.

| Path | Contents |
|-|-|
| `tui/transport.py` | The HTTP session that holds the cookie, and the socket that carries frames. |
| `tui/protocol.py` | The protocol client: requests, pushes, cursors and gap repair. |
| `tui/app.py` | The interface: sidebar, one conversation, composer, commands. |

Three things worth knowing:

- **The room you have selected is the room you occupy.** A place is something you are *in*, so switching away leaves it and quitting leaves everything. For a transient room that is not decoration: its life is measured from the moment its last occupant goes.

- **Everything the desktop expressed by dragging is a command.** Membership used to be edited by dropping one window onto another, which no keyboard could reach and no script could call. `/invite` says what it does, can be refused with a reason, and reads the same in a log. `@name` names a group, which the old interface could not express at all.

- **Three threads, kept apart on purpose.** The socket reader never blocks, because repairing a gap means making a request and a request waits on the reader -- doing it there would deadlock the client against itself. Pushes go to a queue that a separate thread drains.

## API

A client talks to these routes. Shapes still match `@osjs/server`: the contract is frozen, and `tests/conformance/test_http.py` pins every URL, body shape and status against whichever server is running.

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

Three things about the responses:

- Every one carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and a `default-src 'self'` CSP with no `unsafe-inline`; `connect-src` names the request's own host, because the websocket is `ws://` while the page is `http://`.

- `readfile` reports the file's real mime, but serves anything outside a small inline-safe set as an attachment. Inline-safe is images and `text/plain`, with `image/svg+xml` excluded by name -- it is an image that carries script. A document rendered inline from this origin could script it and reach the whole `/vfs` API with the viewer's cookie. Nothing in the UI depends on inline: the viewer reads text through `fetch` and images through `<img>`, and a disposition affects neither.

- `POST /settings` requires a JSON object and answers 400 for anything else. The file is a flat map of namespaces, replaced wholesale, so a payload of another shape would destroy them.

## WebSocket

One socket carries every named message, as JSON `{name, params}` frames. It requires a session; an anonymous upgrade is closed with 1008.

Server to client:

- `osjs/core:connected` on connect, carrying the session lifetime.

- `osjs/core:ping` after 30 seconds of client silence.

- `osjs/application:socket:message` for chat traffic, both replies and unsolicited pushes. `Registry.broadcast` fans these out; a frame whose `pid` is null is a push rather than an answer.

Client to server: only `osjs/application:socket:message` is accepted. Every other `osjs*` name is refused, so a page cannot forge core events. A frame carries `{pid, name, args}`, and `name` selects the handler:

```python
registry = app.extensions["sockets"]

registry.register_application_handler("Echo", lambda conn, respond, args: respond(*args))
```

Handlers belong to one application rather than to the process, so two apps in a
single interpreter -- which the test suite does routinely -- cannot take over
each other's.

`respond` answers the one connection, quoting the `pid` back so the caller can match the reply to its request. `server/chat.py` is the one handler that ships; the section below is what it does.

## Rooms, groups and channels

The model is specified in [chat-concepts.md](chat-concepts.md), which is worth reading before changing any of it. What follows is what the code does.

**A room is a place.** Its identity is its own, not the set of people in it: adding or removing someone leaves the same room, and two rooms may hold the same people and keep separate histories. That last part used to be impossible by definition -- a room *was* its membership, so the client had to search for an existing pair before opening one, and the project's distinctive idea lived in the view layer where a second front end could not reach it.

Two independent facts describe every room, and nothing else about one varies:

- **Authority** -- who founded it, and therefore who may invite. Permanent rooms are created and populated by administrators; they are institutional, so their membership is an administrative fact. Ad-hoc rooms are raised by anyone, and any participant may bring in another.
- **Retention** -- whether it is kept. A persisted room lasts until deleted. A transient room is deleted a grace period after its last occupant leaves, and retains nothing: there is no conversion that rescues what was said, because a promise of discard that somebody can withdraw is not a promise.

**Admission is by invitation, and an invitation names a user or a group.** There is no self-join and no directory. A grant to a group *tracks* that group: assigning somebody admits them everywhere it was invited, without a second invitation, and unassigning revokes the same. That is what groups are for, and it is why editing one is more consequential than it looks.

Two distinctions do real work and are easy to lose:

- **Access is not occupancy.** Who *may* be in a room and who *is* are different facts, and only the second can end -- people do not resign from a conversation, they stop being in it. A transient room's whole lifetime is measured by it.
- **A name is not a description.** A permanent room's title is a name: institutional, chosen, and unique, because "post it in Engineering" only means something if that resolves to one room. An ad-hoc room's title renders who is in it, need not be unique, and is disambiguated by when the room began.

**A channel is the same storage with a different door.** Subscribers choose to subscribe and may not write. The `system` channel carries what the server does -- every VFS write, mkdir and rename -- with the server as its only producer. Letting users submit for a moderator's approval is specified in `chat-concepts.md` and deliberately not built.

### How a message travels

```
send ─┬─> timeline.append ....... assigns the room's next sequence (SQLite)
      │
      └─> bus.publish ──> XSUB ─ zmq.proxy ─ XPUB ──> relay ──> deliver ──> websockets
                                     │                  │          │
                            one owning process    one per worker   host callback;
                                                                   here Registry.broadcast
```

| Module | Responsibility |
|-|-|
| `messaging/timeline.py` | The source of truth. Groups, rooms, grants, messages, occupancy, read state, in SQLite. |
| `messaging/bus.py` | The ZeroMQ leg: an XSUB/XPUB forwarder, and a per-process publisher and relay. |
| `messaging/service.py` | The operations. `sync`, `history`, `send`, `open_room`, `create_room`, `invite`, `leave`, `enter`, `exit`, `sweep`. |
| `server/chat.py` | The adapter: operation names in, OS.js frames out. Owns who is an administrator, and the occupancy a connection holds. |
| `tui/protocol.py` | The protocol client, including gap repair. |
| `tui/app.py` | The sidebar, the conversation, and the commands that replaced the drag targets. |

`messaging/` does not import `server/`, Flask, or anything about who has an
account here. Deliveries leave through a `deliver(audience, event)` callback the
host supplies, the roster of who exists is a `roster()` the host supplies, and a
refusal is a `MessagingError` the host is expected to catch. `tests/test_messaging_independence.py`
holds that line: it parses every module for a forbidden import, imports the
package in a subprocess to prove the server never loads with it, and drives a
whole conversation with delivery going to a list.

```python
from messaging import Broker, Bus, Messaging, Timeline

timeline = Timeline(db_path="var/timeline.db").init()
Broker(xsub, xpub, run_dir="var").start()   # first process wins; the rest connect
bus = Bus(xsub, xpub)

service = Messaging(
    timeline, bus,
    deliver=lambda audience, event: ...,     # hand to your own connections
    roster=lambda: {"alice", "bob"},         # who exists, your business
)
bus.start(service.on_bus_message)
```

Nothing in `messaging/` touches the connection registry. An operation appends to the timeline, publishes, and returns; the relay in each worker decides who is locally connected and hands them to `deliver`. That is what makes a second worker work at all, and it is why the layer never had to learn what a websocket is.

The first process to start claims the forwarder by taking an exclusive `flock` on `.run/broker.lock` and runs `zmq.proxy` in a thread; the rest connect to it. A lock rather than a bind attempt, so a `kill -9` cannot lock everyone out behind a stale `ipc://` file. `python -m server.broker` runs it standalone if you would rather it not live inside a worker.

### Why sequence numbers

PUB/SUB drops rather than queues. A high-water mark, a socket that has not finished connecting, a client offline for an hour -- all three lose messages, and none of them report it. (A subscription that has not propagated used to be a fourth; `messaging/bus.py` now subscribes the proxy to everything and filters where the message lands, so a subscription holds the moment it is asked for.)

Every message therefore gets a per-room sequence number, assigned inside a `BEGIN IMMEDIATE` transaction so concurrent writers in different processes cannot collide. The client keeps a cursor per room and compares:

- at or below the cursor: seen already, drop it
- exactly one above: the next message, render it
- higher: something never arrived -- ask for everything past the cursor

One mechanism covers a dropped frame, a slow joiner and a reconnect, and it is why the client can treat the bus as unreliable without any of it showing. `tests/test_bus.py` and `tests/test_tui.py` are mostly about these edges: a burst must start one backfill rather than one per message, and a subscription to `room.1` must not deliver `room.11`.

The sequence a room issues is stored on the room (`rooms.high_seq`) rather than derived from the messages still present. Those agree today, because nothing removes a message without removing its room. They stop agreeing the moment retention does -- a room trimmed to empty would report zero and reissue numbers a client had already seen, and the client would judge them stale and drop them in silence. One column, and it is what lets the archival in `chat-concepts.md` be built later without breaking every client.

The delivery cursor is not the read cursor. The first answers *what have I received* and lives in the client to repair gaps; the second answers *what has this person seen*, and the server keeps it because it is the same fact from every device. Conflating them marks a message read by arriving.

A backfill is capped at the tail (`HISTORY_LIMIT`, 200 messages) and that cap is
not a window to page through -- asking again from the same cursor returns the
same slice. So a client further behind than the limit gets the newest slice and
nothing before it, and the reply starts more than one past its cursor. That is a
gap which will never be filled, and closing it silently would defeat the whole
mechanism, so the client marks it in the log instead:

```
896 earlier message(s) not shown
```

The reply carries the room's `lastSeq` alongside its messages, which is what
makes the shortfall detectable rather than invisible.

## Scope

This is a demo, not a deployment.

- Credentials are a plaintext dict in `server/config.py`; the session key defaults to a fixed string. Both need replacing before the server is exposed.

- The Flask development server handles the websocket. It is threaded, which is enough for a demo; anything real wants gunicorn with a gevent worker.

- Administrators are a set of names in `server/config.py`, reaching the rest of the server on the session profile's `groups`. Same kind of placeholder as the credentials above.

- VFS changes are announced onto the `system` channel, but only the ones a request made. Nothing watches the filesystem itself, and `osjs/vfs:watch:change` still has no consumer.

- Every account subscribes to the `system` channel, so one user's file paths are visible to all of them -- `demo wrote home:/notes.txt` shows up in alice's client. That is deliberate here, because a channel nobody else can see demonstrates nothing, and filenames are often the sensitive part. Scope the audience to the acting user before this carries anyone's real files.

- The timeline database has no migrations. `Timeline.init` only creates what is missing, so a `.run/timeline.db` written before the model changed is neither upgraded nor rejected. Delete `.run/` when the schema moves.

- Presence is a row per connection rather than a heartbeat. A worker killed outright leaves its rows behind until the next worker starts and reclaims them: each worker holds a `flock` on `.run/worker-<id>.lock` while it runs, so a lock that can be taken belongs to a worker that is gone. Between the kill and that restart the roster still shows its users as online.

- The bus is not authenticated. Anything that can reach the `ipc://` sockets in `.run/` can publish to any room, so a multi-host deployment wants `tcp://` with CURVE rather than the defaults.

- Retention beyond the transient room is specified and not built: archival, the admin's read of it, and the submission workflow that would make a channel curated rather than merely broadcast. All three are in `chat-concepts.md` under *Later*, along with what the core does to avoid foreclosing them.

