# minos

A conversation server in Go, a terminal client, and a frozen wire contract between them.

All three live in `go/`: the server `minosd`, the terminal client `minos`, and `go/conformance/`, which holds the server to [docs/wire-contract.md](docs/wire-contract.md) over HTTP and a websocket alone.

It started as the [OS.js](https://www.os-js.org) v3 client against a Python server that reimplements the contract OS.js expects. Both front ends and the Python server have been removed since. The server still speaks the OS.js wire format -- route shapes, `osjs/*` websocket message names, and the `osjs:` mountpoint are all kept as-is, which is what lets a front end be replaced without touching the server.

What a room, a group and a channel actually are is settled in [chat-concepts.md](docs/dev/chat-concepts.md), before and independently of any way of reaching them. Read that first if you are changing behaviour rather than code; the short version is that **a room is a place** -- it has its own identity, two rooms may hold the same people, and who may enter is a grant naming a user or a whole group.

## Run

```
make serve # the server on http://127.0.0.1:8000
make tui   # the terminal client, in another shell
```

Go 1.25 or newer builds both. There is no other toolchain.

Log in as `demo` / `demo`. There are also `alice` and `bob`, with passwords to match; a conversation needs two of them, so run `make tui` again in a third shell and log in as another. `demo` is the only administrator, which is what lets it found a permanent room or manage a group.

The server does not need a browser build. It warns and serves the API alone if `dist/` is empty, because the terminal client needs the routes and the websocket rather than a bundle.

Inside the client, `/help` lists every command. The [cheatsheet](#cheatsheet) below groups them, with the keys.

```
make demo # the audience rule, narrated, against a server of its own
```

It launches a server on a database of its own and drives three real websockets, so it neither needs nor disturbs anything you have running.

```
make test         # go test ./..., the wire contract included
make conformance  # the wire contract alone
```

`go/conformance/` talks to a server over HTTP and a websocket and imports none of its code; `isolation_test.go` enforces that. `MINOS_CONFORMANCE_CMD` names the server binary to launch, and the suite builds `cmd/minosd` itself when it is unset. `MINOS_CONFORMANCE_URL` points it at a server already running.

## Cheatsheet

```
make serve                                # the server
make go                                   # build go/minosd and go/minos
./go/minos -user alice -password alice    # a client, logged in; make tui prompts instead
```

The accounts are `demo`, `alice` and `bob`, each with its name as the password. Only `demo` is an administrator. `<who>` is a username, or `@name` for a group.

### Keys

| Key | Does |
|-|-|
| Tab, S-Tab (or ^N, ^P) | Next or previous room, channel or person. Not while you are in a room. |
| Enter | Send what is typed. On a room: go in, and the screen becomes that room. On a person: raise a room with them. On a channel item, with nothing typed: open it, or close it. |
| Up, Down | Move through a channel's items. |
| PgUp, PgDn | Scroll the pane. |
| Esc | Close help or archive results. |
| ^U | Clear the composer. |
| ^C, or ^D on an empty line | Quit. |

### Rooms

| Command | Does |
|-|-|
| `/open <who>...` | Raise a room, kept. |
| `/meet <who>...` | Raise a room that is deleted two minutes after everyone leaves. |
| `/create <title>` | Found a permanent, named room. Admin. |
| `/invite <who>` | Admit a user or group. Admin only in a permanent room. |
| `/uninvite <who>` | Withdraw that grant. |
| `/exit` | Step out of this room, keeping your place in it. |
| `/leave` | Give up your own place in this room. |
| `/rooms`, `/people`, `/groups` | List what there is. |

### Channels

| Command | Does |
|-|-|
| `/subscribe <channel>`, `/unsubscribe` | Join or leave a channel's audience. |
| `subject \| body` | Typed in a channel: post with a headline. Without the bar, the first line is the headline. |
| `/channel new <title> [@group]...` | Found a channel, optionally restricted to groups. Admin. |
| `/channel admit\|revoke <channel> <group>` | Restrict a channel to a group, or lift it. Admin. |
| `/group new <name> [user]...` | Create a group. Admin. |
| `/group add\|rm <group> <user>` | Assign or unassign a member. Admin. |

A `<channel>` is one word: its name with `_` for each space, its id, or the start of its id as `/rooms` prints it. The client knows a channel's name once you subscribe, or once `system` has announced it. `system` is the machine channel. Typing in a channel publishes if you are an admin or one of its moderators, and otherwise submits to them.

### Moderation

| Command | Does |
|-|-|
| `/channel appoint\|dismiss <channel> <user>` | Choose who moderates a channel. Admin. |
| `/queue` | What this channel's moderators have to decide. |
| `/approve <id>`, `/reject <id> [why]` | Decide a submission. Moderator. `<id>` is the prefix `/queue` prints. |
| `/submissions`, `/ack <id>` | Your own submissions, and closing a rejection. |

### Archive

| Command | Does |
|-|-|
| `/archive` | Show this space's setting. |
| `/archive <period>\|never [searchable\|private]` | Set it. Admin, permanent rooms and channels only. A period is `45`, `90s`, `30m`, `12h`, `30d` or `2w`. |
| `/archived [<after>]` | Page through the archive. Admin. |
| `/search <text>` | Search it. Admin always; others when it is searchable. |

### Other

| Command | Does |
|-|-|
| `/help` | Every command. |
| `/quit` | Leave every room and stop. |

## Layout

| Path | Contents |
|-|-|
| `docs/dev/chat-concepts.md` | The model: what a room, group and channel are. Front-end independent. |
| `docs/` | The wire contract, and development notes under `docs/dev/`. |
| `go/` | The server, the terminal client, the conformance suite and the demo. One module. |
| `dist/` | What the `osjs:` mountpoint serves. Optional, and nothing in the tree builds it. |
| `vfs/` | User home directories. Generated. |
| `.run/` | The timeline database. Generated. |
| `TODO.md` | Known work not done. |

## The server

One process. A goroutine per connection, an in-process fan-out, and SQLite for the timeline.

| Path | Contents |
|-|-|
| `go/cmd/minosd` | The entry point: configuration, start-up, shutdown. |
| `go/internal/httpapi` | Routes, the signed session cookie, and the websocket upgrade. |
| `go/internal/socket` | The frame format, the connection registry, and the fan-out. |
| `go/internal/chat` | The layer: operation names to messaging calls, occupancy per connection. |
| `go/internal/messaging` | The operations. Knows nothing about how a caller is connected. |
| `go/internal/timeline` | The store: rooms, grants, messages, and the per-room sequence. |
| `go/internal/vfs` | Mountpoints, path resolution, and file operations. |

Two things worth knowing:

- **A send is a queue push.** Each connection has an outbound queue and one writer goroutine, so a fan-out is never held up by the slowest recipient, and a client that falls too far behind is disconnected rather than waited for.

- **A websocket outlives the request that opened it.** Its lifetime is the server's, not `r.Context()`, which `net/http` may cancel once a connection is hijacked.

## Front end

`go/cmd/minos` is the terminal client, drawn with [tcell](https://github.com/gdamore/tcell) (a terminal cell library, the Go counterpart of curses). It needs no browser and no build beyond `go build`.

| Path | Contents |
|-|-|
| `go/internal/client` | The HTTP session that holds the cookie, the socket that carries frames, and the protocol client: requests, pushes, cursors and gap repair. |
| `go/internal/tui` | The interface: sidebar, one conversation, composer, commands. |
| `go/cmd/minos` | Flags, the login prompt, and handing the terminal to the interface. |

Three things worth knowing:

- **The room you enter is the room you occupy, and the screen becomes that room.** A place is something you are *in*: the sidebar goes, Tab stays put, and `/exit` steps back out. The status line counts other rooms with something new, and invitations wait until you step out. You are in one room at a time on every device, so entering one leaves any other, and quitting leaves everything. For a transient room that is not decoration: its life is measured from the moment its last occupant goes.

- **Everything the desktop expressed by dragging is a command.** Membership used to be edited by dropping one window onto another, which no keyboard could reach and no script could call. `/invite` says what it does, can be refused with a reason, and reads the same in a log. `@name` names a group, which the old interface could not express at all.

- **The socket reader never blocks.** Repairing a gap means making a request, and a request waits on the reader -- doing it there would deadlock the client against itself. Pushes go to a queue that a separate goroutine drains.

## API

A client talks to these routes. Shapes still match `@osjs/server`: the contract is frozen, and `go/conformance/http_test.go` pins every URL, body shape and status against a running server.

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

- `readfile` reports the file's real mime, but serves anything outside a small inline-safe set as an attachment. Inline-safe is images and `text/plain`, with `image/svg+xml` excluded by name -- it is an image that carries script. A document rendered inline from this origin could script it and reach the whole `/vfs` API with the viewer's cookie.

- `POST /settings` requires a JSON object and answers 400 for anything else. The file is a flat map of namespaces, replaced wholesale, so a payload of another shape would destroy them.

## WebSocket

One socket carries every named message, as JSON `{name, params}` frames. It requires a session; an anonymous upgrade is closed with 1008.

Server to client:

- `osjs/core:connected` on connect, carrying the session lifetime.

- `osjs/core:ping` after 30 seconds of client silence.

- `osjs/application:socket:message` for chat traffic, both replies and unsolicited pushes. `Registry.Push` fans these out; a frame whose `pid` is null is a push rather than an answer.

Client to server: only `osjs/application:socket:message` is accepted. Every other `osjs*` name is refused, so a page cannot forge core events. A frame carries `{pid, name, args}`, and `name` selects the handler registered with `Registry.Register`. `respond` answers the one connection, quoting the `pid` back so the caller can match the reply to its request. `go/internal/chat` is the one handler that ships; the section below is what it does.

## Rooms, groups and channels

The model is specified in [chat-concepts.md](docs/dev/chat-concepts.md), which is worth reading before changing any of it. What follows is what the code does.

**A room is a place.** Its identity is its own, not the set of people in it: adding or removing someone leaves the same room, and two rooms may hold the same people and keep separate histories. That last part used to be impossible by definition -- a room *was* its membership, so the client had to search for an existing pair before opening one, and the project's distinctive idea lived in the view layer where a second front end could not reach it.

Two independent facts describe every room, and nothing else about one varies:

- **Authority** -- who founded it, and therefore who may invite. Permanent rooms are created and populated by administrators; they are institutional, so their membership is an administrative fact. Ad-hoc rooms are raised by anyone, and any participant may bring in another.

- **Retention** -- whether it is kept. A persisted room lasts until deleted. A transient room is deleted a grace period after its last occupant leaves, and retains nothing: there is no conversion that rescues what was said, because a promise of discard that somebody can withdraw is not a promise.

**Admission is by invitation, and an invitation names a user or a group.** There is no self-join and no directory. A grant to a group *tracks* that group: assigning somebody admits them everywhere it was invited, without a second invitation, and unassigning revokes the same. That is what groups are for, and it is why editing one is more consequential than it looks.

Two distinctions do real work and are easy to lose:

- **Access is not occupancy.** Who *may* be in a room and who *is* are different facts, and only the second can end -- people do not resign from a conversation, they stop being in it. A transient room's whole lifetime is measured by it.

- **A name is not a description.** A permanent room's title is a name: institutional, chosen, and unique, because "post it in Engineering" only means something if that resolves to one room. An ad-hoc room's title renders who is in it, need not be unique, and is disambiguated by when the room began.

**A channel is the same storage with a different door.** Subscribers choose to subscribe and may not write. The `system` channel carries what the server does -- every VFS write, mkdir and rename -- with the server as its only producer. A channel with moderators also takes submissions, which reach it only when a moderator approves them.

### How a message travels

```
send --> timeline.Append ........ assigns the room's next sequence (SQLite)
     |
     +-> deliver(audience, event) --> Registry.Push --> per-connection queue --> writer goroutine --> websocket
```

`go/internal/messaging` imports nothing from the packages above it. Deliveries leave through a `deliver(audience, event)` callback the host supplies, the roster of who exists is supplied by the host, and a refusal is an error the host is expected to report. `go/cmd/minosd/main.go` is the wiring.

### Why sequence numbers

Delivery is not reliable. A client that falls behind its outbound queue is disconnected, and a client offline for an hour misses everything sent meanwhile. Neither reports what it lost.

Every message therefore gets a per-room sequence number, assigned inside the write transaction that stores it. The client keeps a cursor per room and compares:

- at or below the cursor: seen already, drop it

- exactly one above: the next message, render it

- higher: something never arrived -- ask for everything past the cursor

One mechanism covers a dropped frame, a slow joiner and a reconnect. `go/internal/client`'s tests cover these edges, among them that a burst starts one backfill rather than one per message.

The sequence a room issues is stored on the room (`rooms.high_seq`) rather than derived from the messages still present. Those agree today, because nothing removes a message without removing its room. They stop agreeing the moment retention does -- a room trimmed to empty would report zero and reissue numbers a client had already seen, and the client would judge them stale and drop them in silence. One column, and it is what lets archival remove messages without breaking every client.

The delivery cursor is not the read cursor. The first answers *what have I received* and lives in the client to repair gaps; the second answers *what has this person seen*, and the server keeps it because it is the same fact from every device. Conflating them marks a message read by arriving.

A backfill is capped at the tail (`HistoryLimit`, 200 messages) and that cap is not a window to page through -- asking again from the same cursor returns the same slice. So a client further behind than the limit gets the newest slice and nothing before it, and the reply starts more than one past its cursor. That is a gap which will never be filled, and closing it silently would defeat the whole mechanism, so the client marks it in the log instead:

```
896 earlier message(s) not shown
```

The reply carries the room's `lastSeq` alongside its messages, which is what makes the shortfall detectable rather than invisible.

## Scope

This is a demo, not a deployment.

- Credentials are a plaintext map in `go/internal/config`; the session key defaults to a fixed string. Both need replacing before the server is exposed.

- Administrators are a set of names in the same place, reaching the rest of the server on the session profile's `groups`.

- VFS changes are announced onto the `system` channel, but only the ones a request made. Nothing watches the filesystem itself, and `osjs/vfs:watch:change` still has no consumer.

- Every account subscribes to the `system` channel, so one user's file paths are visible to all of them -- `demo wrote home:/notes.txt` shows up in alice's client. That is deliberate here, because a channel nobody else can see demonstrates nothing, and filenames are often the sensitive part. Scope the audience to the acting user before this carries anyone's real files.

- The timeline database is versioned with `PRAGMA user_version` and upgraded in place. Nothing downgrades: keep a copy of `.run/timeline.db` before trying an older server.