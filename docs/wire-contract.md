# The wire contract

What a minos server must do, stated without reference to the language it is
written in. `server/` is one implementation; `tests/conformance/` decides
whether a second one is correct.

The model behind the vocabulary -- room, group, channel, grant, occupancy -- is
in [chat-concepts.md](../chat-concepts.md) and is not repeated here. This
document covers only what crosses the wire.

Two halves, and they differ in kind:

- **HTTP is frozen.** Route names, payload shapes and the `osjs:` mountpoint
  come from @osjs/client and are not ours to change.
- **The socket is ours**, inside one frozen envelope. Every chat operation
  rides the single message name OS.js left open for an application.

## 1. Configuration

Read from the environment. A server that ignores these cannot be tested.

| Variable | Default | Meaning |
|-|-|-|
| `MINOS_HOST` | `127.0.0.1` | listen address |
| `MINOS_PORT` | `8000` | listen port |
| `MINOS_RUN` | `./.run` | runtime state; contents are the implementation's business |
| `MINOS_VFS` | `./vfs` | root of the `home:` mountpoint |
| `MINOS_DIST` | `./dist` | the `osjs:` mountpoint, and the static build |
| `MINOS_SECRET` | a development value | session cookie signing key |
| `MINOS_ROOM_GRACE` | `120` | seconds a transient room outlives its last occupant |
| `MINOS_WS_PING` | `30` | seconds of client silence before a keepalive frame |

A missing `dist/index.html` is not fatal. The server logs and serves the API
alone; `/` then answers 404 while every other route works.

## 2. Sessions

A cookie session, `SameSite=Lax`, lifetime 12 hours, refreshed on each request.
The profile it carries is the frozen shape:

```json
{"id": "demo", "username": "demo", "name": "demo", "groups": ["admin"]}
```

`groups` is the only authority channel. A server must read the role from the
issued session rather than from its user table, so a session keeps the rights
it was issued with.

## 3. HTTP routes

| Method | Route | Body | Answer |
|-|-|-|-|
| GET | `/` | -- | `dist/index.html`, or 404 |
| GET | `/ping` | -- | `ok`, text |
| POST | `/login` | `{username, password}` | the profile, or 403 `{error}` |
| POST | `/logout` | `{}` | `{}` |
| GET | `/settings` | -- | the stored object, `{}` if none |
| POST | `/settings` | any JSON object | `true`; 400 `{error}` if not an object |
| GET/POST | `/vfs/<method>` | see below | per method |

Every route but `/`, `/ping` and `/login` answers 403 `{"error": "Not
authenticated"}` without a session.

Settings are stored at `home:/.osjs/settings.json` and replaced wholesale. A
payload that is not an object is refused rather than stored, because the file
is a flat map of namespaces that clients merge into.

### Errors

One shape, always: `{"error": "<message>"}` with an HTTP status. 400 for a bad
request, 403 for authentication and permission, 404 for a missing path or
method, 409 for a collision.

### Security headers

On every response, uploads included:

- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Content-Security-Policy: default-src 'self'; img-src 'self' data: blob:;
  connect-src 'self' ws://<host> wss://<host>; object-src 'none'; base-uri
  'self'; form-action 'self'; frame-ancestors 'none'`

`connect-src` names the request's own host explicitly: `'self'` does not cover
a `ws://` socket opened from an `http://` page.

## 4. The VFS

Paths are `<mountpoint>:/<path>`. Two mountpoints: `home:` per user, writable;
`osjs:` the build directory, read-only for everyone. A resolved path outside
its mountpoint root is 403, and a write to `osjs:` is 403.

`capabilities`, `exists`, `stat`, `readdir` and `readfile` are GETs with query
parameters; `options` arrives as a JSON string and an unparseable one means
`{}`. Everything else is a JSON POST. `writefile` is `multipart/form-data` with
fields `path` and `upload`.

| Method | Parameters | Answer |
|-|-|-|
| `capabilities` | `path` | `{"sort": false, "pagination": false}` |
| `exists` | `path` | boolean |
| `stat` | `path` | one descriptor; 404 if absent |
| `readdir` | `path` | descriptors, sorted by lowercased name; 404 if not a directory |
| `readfile` | `path`, `options.download` | the bytes |
| `writefile` | `path`, `upload` | bytes written, as a number |
| `mkdir` | `path`, `options.ensure` | `true`; 409 if it exists and `ensure` is false |
| `unlink` | `path` | `true`; 404 if absent; recursive for a directory |
| `touch` | `path` | `true`, creating parents |
| `copy` | `from`, `to` | `true` |
| `rename` | `from`, `to` | `true` |
| `search` | `root`, `pattern`, | descriptors, at most 100 |

A descriptor:

```json
{"isDirectory": false, "isFile": true, "mime": "text/plain", "size": 5,
 "path": "home:/notes.txt", "filename": "notes.txt",
 "stat": {"size": 5, "mode": 33188,
          "atime": "1970-01-01T00:00:00+00:00", "mtime": "...", "ctime": "...",
          "atimeMs": 0.0, "mtimeMs": 0.0, "ctimeMs": 0.0}}
```

`mime` is null for a directory. Times are ISO 8601 UTC, and the `Ms` variants
are milliseconds as floats.

A `search` pattern containing none of `* ? [` is wrapped as `*pattern*`, and
matching is case-insensitive on the filename alone.

### Disposition

`readfile` reports the real MIME type either way, and only the disposition
varies. Inline for `image/*` and `text/plain`; attachment for everything else,
for `image/svg+xml` specifically, and whenever `options.download` is set.

The reason is not presentational. A document served inline from this origin can
script it, and that script reaches the whole `/vfs` API with the viewer's
cookie. SVG is excluded because it is an image that carries script.

## 5. The socket

One websocket at `/`, sharing the path with the index route. The upgrade is
gated on the session: without one the server accepts the upgrade and then
closes with code **1008** and reason `Not authenticated`.

Every frame, both directions:

```json
{"name": "<string>", "params": [...]}
```

### Server to client

| Name | Params | When |
|-|-|-|
| `osjs/core:connected` | `[{"cookie": {"maxAge": <ms>}}]` | immediately, before anything else |
| `osjs/core:ping` | `[]` | after `MINOS_WS_PING` seconds of client silence |
| `osjs/application:socket:message` | see below | replies and pushes |

The handshake is sent *after* the connection joins the fan-out, so a client
that has received it cannot miss a broadcast.

### Client to server

Exactly one name is accepted: `osjs/application:socket:message`. Any other name
beginning with `osjs` is refused as forged and logged -- without it a page could
fabricate `osjs/core:logged-in` and drive handlers that trust it. Anything else
is dropped as unhandled. A malformed frame, a non-string `name`, or a `params`
that is not a list is dropped. **None of these close the socket.**

### The application envelope

Outbound from the client:

```json
{"name": "osjs/application:socket:message",
 "params": [{"pid": 7, "name": "Chat", "args": [{"op": "sync"}]}]}
```

A reply quotes the `pid` and carries no `name`:

```json
{"name": "osjs/application:socket:message",
 "params": [{"pid": 7, "args": [{...}]}]}
```

A push has `pid: null` and does carry the application name:

```json
{"name": "osjs/application:socket:message",
 "params": [{"pid": null, "name": "Chat", "args": [{"type": "message", ...}]}]}
```

`pid` is opaque to the server: whatever arrives is quoted back, which is what
lets a client have several requests in flight. A handler that throws answers
`{"error": "Request failed"}` rather than closing the socket -- one bad field
must not cost a client its connection.

An application with no handler produces no reply at all. A client must
therefore time out rather than wait forever.

## 6. Chat operations

`args[0].op` selects one. An unknown op answers
`{"error": "No such chat operation: <op>"}`. A refusal is always
`{"error": "<message>"}` and never a status code -- the socket has none.

| Op | Request fields | Reply |
|-|-|-|
| `sync` | -- | the sync object |
| `history` | `room`, `since` | `{room, since, lastSeq, messages}` |
| `send` | `room`, `body` | `{ok: true, seq}` |
| `open` | `invite`, `title`, `retention` | a room |
| `create` | `title`, `invite` | a room |
| `invite` | `room`, `principal` | `{ok: true, room}` |
| `uninvite` | `room`, `principal` | `{ok: true, room}` |
| `leave` | `room` | `{ok: true}` |
| `enter` | `room` | `{ok: true, occupancy, room}` |
| `exit` | `occupancy` | `{ok: true}` |
| `read` | `room`, `seq` | `{ok: true, room, seq}` |
| `group.create` | `name`, `members` | a group |
| `group.assign` | `group`, `username` | a group |
| `group.unassign` | `group`, `username` | a group |
| `subscribe` | `channel` | a channel |
| `unsubscribe` | `channel` | `{ok: true}` |
| `channel.create` | `title`, `groups` | a channel |
| `channel.publish` | `channel`, `body` | `{ok: true, seq}` |
| `channel.admit` | `channel`, `group` | `{ok: true, channel}` |
| `channel.revoke` | `channel`, `group` | `{ok: true, channel}` |

### Objects

A room, and a channel in the same shape:

```json
{"id": "<hex>", "title": "Engineering", "kind": "room",
 "authority": "admin", "retention": "persisted",
 "createdBy": "demo", "createdAt": 1757030400.0,
 "grants": [{"kind": "user", "id": "demo"}], "restrictedTo": [],
 "audience": ["alice", "demo"], "occupants": [], "lastSeq": 4}
```

`kind` is `room` or `channel`; `authority` is `admin` or `user`; `retention` is
`persisted` or `transient`. A channel has empty `grants` and its `audience` is
its subscribers. `audience` resolves group grants, so it lists users and never
groups. `occupants` is who is in the room now, not who may be. `restrictedTo`
is the groups a channel admits, empty on an open channel and on every room.

A message:

```json
{"room": "<id>", "seq": 3, "author": "alice", "kind": "text",
 "body": "hello", "at": 1757030400.0}
```

`kind` is `text` for what a person typed, `event` for what happened to the
room. An `event` has author `system`. `at` is a Unix timestamp as a float.

A group: `{"id": "<hex>", "name": "Team", "members": ["alice"]}`.

Sync:

```json
{"me": "demo", "isAdmin": true,
 "users": [{"username": "alice", "online": false}],
 "groups": [...], "rooms": [...], "channels": [...],
 "read": {"<room id>": 4}}
```

`users` is every account, sorted; `groups` sorted by name. `read` omits rooms
with no cursor.

### Rules a reimplementation must reproduce

- `create` is administrators only, always persisted, and its title is unique
  among permanent rooms, compared without case. A `retention` field sent with it
  is ignored rather than refused, so no room is both admin-founded and
  transient. `open` is open to anyone and its title is not unique.
- `open` with no title derives one from the sorted principal names.
- `retention` must be `persisted` or `transient`; anything else is refused.
- Only an administrator may invite to an admin-founded room. Any participant
  may invite to a user-founded one.
- `leave` removes a grant naming the user directly. Access inherited from a
  group is refused, because dropping it would be restored the moment grants
  were re-evaluated.
- `send` to a channel is refused: a channel is read-only to its audience.
- `channel.create` is administrators only. Its title is unique among channels,
  compared without case, for the reason a permanent room's is among rooms.
  `groups` sets the audience rule at once and each must exist. Nothing is pushed
  to the new channel -- it has no subscribers -- and the server announces it on
  `system` instead, as `<user> opened the channel <title> (<id>)`.
- `channel.publish` is administrators only and writes as its caller, `kind`
  `text`. It is the producer's path: a channel's producer is the administrator,
  as the machine is `system`'s, and `send` to a channel stays refused whoever
  sends it.
- A channel with no `restrictedTo` groups is open to anybody. One with groups
  admits their members only: `subscribe` from anyone else is refused with
  `That channel is restricted`, and `channel.admit` / `channel.revoke` are
  administrators only. A channel never names a user, only groups.
- Eligibility is re-read on every delivery, not fixed when the subscription was
  stored. Someone removed from the last group that admitted them keeps the
  subscription and leaves the `audience`: they stop receiving the channel, it
  leaves their `sync`, `history` on it is refused, and they get a `roomGone`.
  Re-admitting the group restores all of it without their acting again.
- Revoking the last group leaves the channel open rather than closed to
  everybody: open is the absence of a rule, so there is no way to write "nobody"
  and no reason to.
- An empty or whitespace-only body is refused.
- `exit` may only release an occupancy the same connection took. Another
  connection's is refused with `Not in that room`.
- `read` never moves a cursor backwards.
- An unparseable `since` or `seq` means zero rather than an error.
- A room id that is not a string is "no such room", not a type error.

### Lifecycle

- A connection that closes releases every occupancy it held.
- Presence is announced on connect and on disconnect.
- A transient room with no occupants is deleted `MINOS_ROOM_GRACE` seconds
  later, and its audience is told before it goes.
- The `system` channel exists at start-up, titled `System`, with every account
  subscribed. The server is its only producer.
- A successful VFS mutation posts an event to `system`:
  `<user> wrote|created|deleted|touched|renamed|copied <path>`. A failure to
  post is swallowed; the mutation has already happened.

## 7. Pushes

`args[0].type` selects one. All arrive with `pid: null`.

| Type | Payload | Sent to |
|-|-|-|
| `message` | a message object | the room's audience |
| `room` | `{"room": <room>}` | the audience, plus anyone just removed from it |
| `roomGone` | `{"room": "<id>"}` | whoever must drop it |
| `presence` | `{"username", "online"}` | everyone but its subject |
| `group` | `{"group": <group>}` | everyone |

A `room` push carries the whole object rather than a delta, so a client that
missed one is not left behind. It is what tells a newly invited user the room
exists at all.

Presence names no room, and there is no per-room presence event. Where a user is
travels on the `room` push instead, in `occupants`: presence says whether someone
could reply, occupancy says who is in this room, and only the second decides when
a transient room dies.

`roomGone` covers three causes and does not distinguish them: a transient room
expired, a user unsubscribed from a channel, or a group assignment was
withdrawn and took the user's last access with it.

## 8. Delivery

**The bus is not reliable and the sequence number is what repairs it.** A push
may be dropped, duplicated or reordered. A message's `seq` is monotonic within
its room and assigned by the server at the moment it is stored, never by a
publisher.

A client keeps a cursor per room and applies an arriving message by comparing:

- `seq <= cursor` -- already seen, discard
- `seq == cursor + 1` -- the next message, apply
- `seq > cursor + 1` -- something never arrived; discard this copy and request
  `history` from the cursor

Discarding is safe because the server stores before it publishes, so the
backfill contains what was dropped.

`history` is capped at 200 and the cap is on the **tail**: a client a thousand
messages behind receives the newest two hundred. Asking again from the same
cursor returns the same slice, so the shortfall is not pageable -- it is
detectable instead. The first `seq` returned sits above the cursor, and
`lastSeq` says where the room actually ends. A client that finds a shortfall
must say so rather than advancing its cursor over messages it never received.

This contract is transport-independent, which is the reason it survives any
change of transport: it covers a reconnect, an hour offline and a lagging
receiver as well as it covers a dropped frame.

## 9. Not part of the contract

An implementation may do any of this differently:

- the message bus, its topics, and whether one exists at all
- the storage engine, its schema, and everything in `MINOS_RUN`
- worker processes, leases, liveness locks and their sweep
- threading, connection registries, and how a fan-out finds its recipients
- the `pid` a server chooses for a push, provided it is null
- whether an `osjs*` frame other than the application message is *refused* as
  forged or merely *dropped* as unhandled. Only the outcome is observable, and
  the outcome is the same: nothing happens
