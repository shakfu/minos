# The wire contract

What a minos server must do, stated without reference to the language it is
written in. `go/` implements it, and `go/conformance/` decides whether it is
correct.

The model behind the vocabulary -- room, group, channel, grant, occupancy -- is
in [chat-concepts.md](dev/chat-concepts.md) and is not repeated here. This
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
| `MINOS_ROOM_UNENTERED` | `900` | seconds a transient room lasts that nobody has entered |
| `MINOS_ROOM_SWEEP` | `15` | seconds between sweeps for expired rooms and due archival |
| `MINOS_WS_PING` | `30` | seconds of client silence before a keepalive frame |

A missing `dist/index.html` is not fatal. The server logs and serves the API
alone; `/` then answers 404 while every other route works.

## 2. Sessions

A cookie session, `SameSite=Lax`, lifetime 12 hours, refreshed on each request,
`/ping` included. However often it is refreshed, a session ends 7 days after
login.

`/logout` ends the session on the server: the same cookie is refused afterwards,
and every websocket the session opened is closed. A websocket also closes when
its session reaches the 7-day limit. A server may forget a logout when it
restarts; the limit still applies.

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
method, 409 for a collision, 413 for a body over its limit, 415 for a JSON POST
whose `Content-Type` is not `application/json`.

A `writefile` body is at most 100 MiB, and any other request body at most 1 MiB.

The 415 refusal reads `Requests must be JSON` and comes after the session check.
A page on another origin can send `text/plain` without a CORS preflight, but not
`application/json`.

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
its mountpoint root is 403, and a write to `osjs:` is 403. A symlink that leads
outside its mountpoint is not followed; the path reads as absent.

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
| `unlink` | `path` | `true`; 404 if absent; recursive for a directory; 403 for a mountpoint's root |
| `touch` | `path` | `true`, creating parents |
| `copy` | `from`, `to` | `true`; 400 if `to` is `from`, or lies inside it |
| `rename` | `from`, `to` | `true`; 403 if either is a mountpoint's root |
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

An upgrade whose `Origin` names another host is refused with 403 before it
completes. An upgrade with no `Origin`, as a client that is not a browser sends,
is accepted.

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
that is not a list is dropped. **None of these close the socket.** The exception
is a frame over 1 MiB, which closes it with code **1009**.

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
Section 9 adds `moderators`, section 11 adds `archive`, section 12 adds
`project`, `scope` and `task`, and section 13 adds `state`.

A message:

```json
{"room": "<id>", "seq": 3, "author": "alice", "kind": "text",
 "body": "hello", "at": 1757030400.0}
```

`kind` is `text` for what a person typed, `event` for what happened to the
room. An `event` has author `system`. `at` is a Unix timestamp as a float.
Section 10 adds `subject`.

A group: `{"id": "<hex>", "name": "Team", "members": ["alice"]}`.

Sync:

```json
{"me": "demo", "isAdmin": true,
 "users": [{"username": "alice", "online": false}],
 "groups": [...], "rooms": [...], "channels": [...],
 "read": {"<room id>": 4}, "visited": ["<room id>"]}
```

`users` is every account, sorted; `groups` sorted by name. `visited` is every
room the caller has ever entered, sorted. `read` omits rooms
with no cursor. Section 9 adds `submissions`, section 10 removes channels
from `read`, and section 12 adds `projects`.

### Rules a reimplementation must reproduce

- `create` is administrators only, always persisted, and takes `project`,
  `scope` and `task` as section 12 defines them: a permanent room is founded
  where it belongs rather than filed afterwards, because its title is unique
  only within its project. That title is unique among the permanent rooms of
  the project it is founded in, compared without case; rooms under no project
  are a bucket of their own. A `retention` field sent with it is ignored rather
  than refused, so no room is both admin-founded and transient. `open` is open
  to anyone and its title is not unique.
- `open` with no title derives one from the sorted principal names.
- `retention` must be `persisted` or `transient`; anything else is refused.
- Only an administrator may invite to an admin-founded room. Any participant
  may invite to a user-founded one.
- `leave` removes a grant naming the user directly. Access inherited from a
  group is refused, because dropping it would be restored the moment grants
  were re-evaluated.
- A room with no grants is reachable by nobody. So `leave` or `uninvite` of an
  ad-hoc room's last grant deletes the room, and `uninvite` answers
  `{ok: true, room: null}`. The last grant of a permanent room is refused with
  `A permanent room keeps its last grant`.
- Losing access to a room, by `leave`, `uninvite` or `group.unassign`, releases
  every place the user held in it, and the user is sent a `roomGone`.
- `send` to a channel is refused: a channel is read-only to its audience.
- `channel.create` is administrators only and takes `project`. Its title is
  unique among the channels of that project, compared without case, for the
  reason a permanent room's is among rooms. `groups` sets the audience rule at
  once and each must exist. Nothing is pushed
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
- An empty or whitespace-only body is refused. A body over 64 KiB, as UTF-8
  bytes, is refused with `A message is at most 65536 bytes`; so is a
  `submission.reject` comment.
- A title given to `open`, `create` or `channel.create`, and a group's name, is at
  most 200 characters, refused with `A name is at most 200 characters`.
- `exit` may only release an occupancy the same connection took. Another
  connection's is refused with `Not in that room`.
- A user occupies at most one room. `enter` first releases every occupancy the
  user holds in another room, on any connection: each room left is announced
  with a `room` push, and the user is sent `exited` for each occupancy
  released. Occupancies in the entered room, on other connections, are kept.
- `exit` of an occupancy already released answers `{ok: true}`.
- `enter` records a visit. The room stays in the caller's `visited` from then
  on, whatever becomes of their access.
- `read` never moves a cursor backwards.
- An unparseable `since` or `seq` means zero rather than an error.
- A room id that is not a string is "no such room", not a type error.

### Lifecycle

- A connection that closes releases every occupancy it held.
- Presence is a user's, not a connection's. It is announced when a user's first
  connection opens and when their last one closes, and not in between.
- A transient room whose last occupant has left is deleted `MINOS_ROOM_GRACE`
  seconds later, and its audience is told before it goes.
- A transient room that nobody has entered is deleted `MINOS_ROOM_UNENTERED`
  seconds after it was raised, and its audience is told the same way.
- The `system` channel exists at start-up, titled `System`, with every account
  subscribed. The server is its only producer. `channel.publish` and
  `channel.appoint` on it are refused with `Only the server writes to system`,
  and `unsubscribe` with `Every account receives system`.
- A successful VFS mutation posts an event to `system`:
  `<user> wrote|created|deleted|touched|renamed|copied <path>`, with each control
  character in the path replaced by `?`. A failure to
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
| `project` | `{"project": <project>}` | everyone |
| `projectGone` | `{"project": "<id>"}` | everyone |
| `exited` | `{"room": "<id>", "occupancy": "<id>"}` | the user whose occupancy `enter` released |

A `room` push carries the whole object rather than a delta, so a client that
missed one is not left behind. It is what tells a newly invited user the room
exists at all.

Presence names no room, and there is no per-room presence event. Where a user is
travels on the `room` push instead, in `occupants`: presence says whether someone
could reply, occupancy says who is in this room, and only the second decides when
a transient room dies.

`roomGone` covers four causes and does not distinguish them: a room was deleted,
by expiry or with its last grant; a user unsubscribed from a channel; a grant was
withdrawn by `leave` or `uninvite`; or a group assignment was withdrawn and took
the user's last access with it. A user who keeps access another way is not sent one.

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

## 9. Beyond the core: submissions and moderation

[chat-concepts.md](dev/chat-concepts.md) section 5.

### Fields added to core shapes

- A room or channel carries `moderators`: the usernames that moderate a channel,
  sorted. Empty on a channel that takes no submissions, and on every room.
- `sync` carries `submissions`: the caller's own, pending or rejected and not
  yet acknowledged, oldest first.

### Operations

| Op | Request fields | Reply |
|-|-|-|
| `channel.appoint` | `channel`, `username` | `{ok: true, channel}` |
| `channel.dismiss` | `channel`, `username` | `{ok: true, channel}` |
| `channel.submit` | `channel`, `body` | a submission |
| `channel.queue` | `channel` | `{channel, submissions}` |
| `submission.approve` | `submission` | `{ok: true, seq}` |
| `submission.reject` | `submission`, `comment` | `{ok: true}` |
| `submission.acknowledge` | `submission` | `{ok: true}` |

A submission:

```json
{"id": "<hex>", "channel": "<id>", "author": "bob", "body": "a tip",
 "at": 1757030400.0, "state": "pending", "comment": null}
```

`state` is `pending` or `rejected` while stored. `approved` appears only on a
push, because approval turns a submission into a message and deletes it.
`comment` is null unless a moderator rejected it with one.

### Rules

- A submission takes no sequence number. Approval appends it to the channel as a
  `text` message authored by the submitter, with the next `seq`. Submitting and
  rejecting move nothing, so the channel's sequence stays contiguous.
- A channel with no moderators accepts no submissions. `channel.submit` to one
  is refused with `That channel accepts no submissions`.
- `channel.appoint` and `channel.dismiss` are administrators only, and each
  announces the channel with a `room` push. Appointing names a user that exists.
- `channel.queue`, `submission.approve` and `submission.reject` are the channel's
  moderators only, refused with `Only a moderator may do that`. An administrator
  is not implicitly a moderator and may appoint themselves.
- A moderator may `channel.publish`. On a channel with moderators, anyone who is
  neither a moderator nor an administrator is refused with `Only an
  administrator or a moderator may do that`. A channel without moderators
  refuses as the core does.
- `channel.submit` needs access to the channel, as `history` does, and a body
  that is not empty once trimmed.
- A decided submission is refused with `That submission has been decided`.
- A rejected submission stays until its author sends `submission.acknowledge`,
  which deletes it. A pending one is refused with `That submission is still
  pending`, and anyone else's reads as `No such submission: <id>`.
- Dismissing the last moderator rejects every pending submission with the
  comment `That channel no longer accepts submissions`.

### Pushes

| Type | Payload | Sent to |
|-|-|-|
| `submission` | `{"submission": <submission>}` | the moderators on submit; the author and the moderators on a decision |

## 10. The channel feed

[chat-concepts.md](dev/chat-concepts.md) 2.4 and 2.5.

### Fields added to core shapes

- A message carries `subject`. In a channel it is never empty. In a room it is
  `null`.
- A submission carries `subject`, under the same rules as a channel message.
- On a channel, a `history` reply carries `opened`: the `seq`s among its
  `messages` that the caller has opened, ascending.
- `sync`'s `read` omits channels.

### Operations

| Op | Request fields | Reply |
|-|-|-|
| `channel.publish` | `channel`, `subject`, `body` | `{ok: true, seq}` |
| `channel.submit` | `channel`, `subject`, `body` | a submission |
| `channel.open` | `channel`, `seq` | `{ok: true, channel, seq}` |

`subject` is optional in both writes.

### Rules

- A subject is trimmed. An empty one is replaced by the first line of the
  trimmed body, cut to 200 characters. A given subject over 200 characters is
  refused with `A subject is at most 200 characters`.
- `channel.publish` and `channel.submit` are refused with `Empty message` only
  when subject and body are both empty once trimmed. For these two operations
  this replaces the empty-body rule in section 6.
- An event the server writes to a channel follows the same rule, so its subject
  is its event line and its body is unchanged.
- A message is opened for its author when it is published. For an approved
  submission, the author is the submitter.
- `channel.open` needs access to the channel, as `history` does, and is
  idempotent. A `seq` that is not a live message in the channel is refused with
  `No such message: <seq>`. On `system` it is refused with `Nothing in system is
  opened`, and on a room with `No such room: <id>`, as the other `channel.*`
  operations are.
- `read` on a channel is refused with `A channel's items are opened, not read`.
- A caller's pending stack is the channel's live messages they have not opened.
  The server does not report its size.

### Pushes

| Type | Payload | Sent to |
|-|-|-|
| `opened` | `{"channel": "<id>", "seq": n}` | the caller's own connections |

## 11. Archival

[chat-concepts.md](dev/chat-concepts.md) section 4.

### Fields added to core shapes

A room or channel carries `archive`: `{"period": <seconds or null>,
"searchable": <bool>}`. A user-created room always carries `{"period": null,
"searchable": false}`.

### Operations

| Op | Request fields | Reply |
|-|-|-|
| `archive.set` | `room`, `period`, `searchable` | `{ok: true, room}` |
| `archive.read` | `room`, `after` | `{room, after, messages, more}` |
| `archive.search` | `room`, `query` | `{room, query, messages}` |

`room` names a room or a channel.

### Rules

- `archive.set` is administrators only, on a permanent room or a channel.
  Anywhere else it is refused with `Only a permanent room or a channel is
  archived`. From anyone but an administrator it is refused with `Only an
  administrator may do that`. An absent field keeps its value. `period` is a
  positive number of seconds, or null for never; anything else is refused with
  `A period is a positive number of seconds`. `searchable` is a boolean; anything
  else is refused with `Searchable is true or false`. The space is announced
  with a `room` push.
- Every `MINOS_ROOM_SWEEP` seconds, each space with a period archives its longest
  run of live messages, from the lowest `seq`, whose `at` is at least `period`
  seconds ago. A changed period applies at the next sweep. Nothing is restored.
- Archival neither moves `lastSeq` nor renumbers anything. `history` returns live
  messages only, so a cursor below the first live `seq` meets a shortfall
  (section 8).
- Archiving a message deletes every opened mark on it.
- `archive.read` is administrators only, refused to anyone else as `archive.set`
  is. It returns up to 200 archived messages
  with `seq` above `after` (default 0), ascending. `more` says whether others
  follow.
- `archive.search` is open to administrators always, and to the space's audience
  when `searchable` is true. The rest of the audience is refused with `That
  archive is not searchable`; anyone outside it is refused as `history` refuses
  them. `query` is trimmed, and an empty one is refused with `Empty query`. It
  matches archived messages
  whose subject or body contains it, compared without case: at most 100, newest
  first.
- An archived message has the message shape, unchanged.

### Pushes

| Type | Payload | Sent to |
|-|-|-|
| `archived` | `{"room": "<id>", "through": n}` | the space's audience |

A client drops its copies of the messages up to `through`.

## 12. Projects

A project is a named container of rooms and channels. It holds no messages and
decides no access, so it has no audience: every caller sees every project. A
room is filed under at most one, and `project` on a room object names it.

Depth is one. A project contains rooms, never other projects.

### Fields added to core shapes

A room and a channel gain three:

| Field | Meaning |
|-|-|
| `project` | the project it is filed under, or `""` |
| `scope` | `project`, `task`, or `""` under no project |
| `task` | which task, on a task room; `""` otherwise |

`task` is opaque. The server stores and returns it and never parses it: which
task it names is the caller's business.

Sync gains `projects`, every project sorted by name without case:

```json
{"projects": [{"id": "<hex>", "name": "cynn", "createdAt": 1757030400.0,
               "tags": ["go", "infra"]}]}
```

`tags` classify a project and decide nothing. They are sorted, lower case, and
each is one word of at most 32 characters.

### Operations

| Op | Request fields | Reply |
|-|-|-|
| `project.create` | `name`, `tags` | `{ok: true, project}` |
| `project.tag` | `project`, `tag` | `{ok: true, project}` |
| `project.untag` | `project`, `tag` | `{ok: true, project}` |
| `project.file` | `room`, `project`, `scope`, `task` | `{ok: true, room}` |
| `project.dissolve` | `project` | `{ok: true}` |

All five are administrator-only and answer
`{"error": "Only an administrator may do that"}` otherwise.

### Rules

- **A name is unique, compared without case, and holds no `/`.** A second
  `project.create` with a name already taken is refused with `There is already
  a project called <name>`; it does not return the existing one. A name with a
  slash is refused with `A project name has no '/' in it`, because a place is
  referred to from outside its project as `<project>/<title>`, split at the
  first slash. A room title may still hold one: everything after that first
  slash is the title, so `cynn/task/31` is `task/31` in `cynn`.
- **A title is unique within its project, not across them.** `design` in `cynn`
  and `design` in `sanduk` are two rooms; a second `design` in `cynn` is
  refused with `A permanent room called 'design' already exists in cynn`, or
  `... already exists under no project` for the unfiled bucket. The qualified
  name is a client's way of saying which; the server carries `project` and
  `title` separately and never parses a slash.
- **`project.file` refuses a move into a project where the title is taken**,
  with the same message. A room already in that project is not moving, so
  re-filing it where it is answers `ok`.
- **A tag is folded to lower case and one word.** `Go` and `go` are the same
  tag, because a tag exists to be filtered on and two that filter apart would
  divide the projects rather than classify them. A tag with a space is refused
  with `A tag is one word`.
- **`project.tag` and `project.untag` answer the state, not the change.**
  Tagging a project that already carries the tag, and untagging one that does
  not, both answer `ok` with the project.
- **`project.file` with an empty `project` files the room under none.** That is
  how a room leaves a project; there is no separate unfile. It clears `scope`
  and `task` with it, because a scope is a position within a project.
- **A scope needs a project, and a task needs a task scope.** `scope` or `task`
  with no project is refused with `A room under no project has no scope`;
  `task` on a project-scoped room with `Only a task room names a task`; a task
  scope with no task with `A task room names its task`. Filing under a project
  without naming a scope gives `project`.
- **A transient room cannot be filed**, and `project.file` on one is refused
  with `A transient room is not filed under a project`. A transient room is
  discarded when everyone leaves, so filing it records a place about to stop
  existing.
- **`project.dissolve` keeps the rooms.** They are filed under none, `scope`
  and `task` cleared, and each gets a `room` push. A project holds no messages,
  so there is nothing in it to lose.
- **Filing a room does not change who may reach it.** No grant, subscription or
  audience moves, and no `roomGone` is sent.

### Pushes

| Type | Payload | Sent to |
|-|-|-|
| `project` | `{"project": <project>}` | everyone |
| `projectGone` | `{"project": "<id>"}` | everyone |

Everyone, as `group` is, because a project decides no access and so has no
narrower audience to send to.

## 13. Open and closed

A room or a channel is `open` or `closed`. Closing says the work in it is done.
It is not deleting and not archiving: the room stays, its messages stay, and
its audience still reads and writes it. What changes is that it is no longer
one of the places work is happening in, which is what a client counts when it
says how many places are active.

### Fields added to core shapes

A room and a channel gain `state`: `open` or `closed`. Everything is created
open.

### Operations

| Op | Request fields | Reply |
|-|-|-|
| `room.close` | `room` | `{ok: true, room}` |
| `room.reopen` | `room` | `{ok: true, room}` |

### Rules

- **Who may close is who may invite.** An administrator for an admin-founded
  room or a channel; any participant for a user-founded room. The refusal is
  the existing `Only an administrator may invite to this room` or `Not invited
  to that room`.
- **A transient room is not closed**, and either op on one is refused with
  `A transient room is not closed; it ends when everyone leaves`. Its grace
  period already decides when it ends, and closing would name a second,
  contradictory end.
- **Closing and reopening are idempotent**, and answer the room either way.
- **A change posts an event** to the room, `<user> closed this room` or
  `<user> reopened this room`, and pushes the room to its audience. Closing a
  room that is already closed posts nothing.

## 14. Not part of the contract

An implementation may do any of this differently:

- the message bus, its topics, and whether one exists at all
- the storage engine, its schema, and everything in `MINOS_RUN`
- worker processes, leases, liveness locks and their sweep
- threading, connection registries, and how a fan-out finds its recipients
- the `pid` a server chooses for a push, provided it is null
- whether an `osjs*` frame other than the application message is *refused* as
  forged or merely *dropped* as unhandled. Only the outcome is observable, and
  the outcome is the same: nothing happens
