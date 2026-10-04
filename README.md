# minos

minos carries the conversation between AI coding agents in sealed containers, the model that supervises them, and the developer who reads and intervenes.

It is one of three tools:

| Tool | Language | Does |
|-|-|-|
| [pma](https://github.com/shakfu/pma) | Rust | Ranks outstanding tasks across many repositories and dispatches each to an agent. |
| [sanduk](https://github.com/shakfu/sanduk) | Python | Runs one agent per container, on an `--internal` network with a host-side relay. |
| minos | Go | Carries the conversation: one room per workflow, submissions for decisions, a terminal client for the developer. |

The design is in [design.md](docs/dev/design.md) and [recommended-architecture.md](docs/dev/recommended-architecture.md). The build order and its status are in [implementation-plan.md](docs/dev/implementation-plan.md).

```text
  HOST                                         SEALED CONTAINER
  developer -- minos (TUI) --.
  supervising model ---------+--> minosd <--- minosb ---- unix socket ---> minosa (shim)
  pma --- starts minosb, stops the container directly     stdin/stdout ---> agent process
```

## Features

### Container runs

- **The credential stays on the host.** `minosb`, one per run, holds the session, the room, the cursor and the agent's pipes. The container holds a socket file and the `minosa` shim.

- **Six operations, and no seventh.** `messages`, `say`, `submit`, `await`, `progress`, `status`. The container cannot reach a file write or a settings change, because the broker has no code for them.

- **Three lanes to the agent.** The broker pushes room messages onto the agent's stdin between turns. It relays each turn's final message from stdout to the room. The agent asks for anything else over the socket.

- **One frame per turn.** Messages held during a turn go as one stdin frame, so one turn answers them.

- **Decisions through a channel.** `submit` posts to a moderated channel; `await` blocks until a moderator approves or rejects it, or the wait times out.

- **Payloads from a file or stdin.** No subcommand takes JSON on its command line. A payload travels in a fenced block beside the text.

- **Tears stop the run.** If the broker falls further behind than the history limit, it reports `torn` and exits rather than skip messages.

- **Control events for the dispatcher.** `minosb` writes `ready`, `held`, `turn`, `interrupt`, `refused`, `torn`, `exited` and `stopped` to stdout as JSON lines.

- **Interrupts.** A turn stops on `{"op":"interrupt","id":"..."}` on `minosb`'s stdin, or on `/interrupt [correction]` in the room from a user named in `-interrupters`. The correction is pushed when the stopped turn ends.

- **Socket hardening.** The socket is created with its final mode, refuses a path holding anything else, has a 5 s request deadline and admits 64 connections.

- **Forgery-safe output.** A message body holding a newline or other non-printable character is quoted in both lanes.

### Conversation server

- **Rooms with their own identity.** Two rooms may hold the same people and keep separate histories.

- **Admission by invitation.** A grant names a user or a group. A group grant follows the group's membership.

- **Permanent and ad-hoc rooms.** Administrators found permanent, uniquely named rooms. Anyone raises an ad-hoc room.

- **Transient rooms.** Deleted `MINOS_ROOM_GRACE` seconds (default 120) after the last occupant leaves.

- **Projects, scope and state.** A room is filed under a project, scoped to the project or to one opaque task label, and marked open or closed.

- **Channels.** Subscribers read; admins and moderators publish. A channel can be restricted to groups.

- **Moderated submissions.** A submission waits outside the channel's sequence until a moderator approves or rejects it.

- **Archival.** Per-room or per-channel retention by age, searchable or private. Sequence numbers are never reissued.

- **Gap repair.** Each message has a per-room sequence number. The client detects a missing one and backfills.

- **Server-side read state.** Read cursors are the same on every device and never move backwards.

- **The `system` channel.** The server announces each VFS write, mkdir and rename made through a request.

- **OS.js wire format.** Routes, `osjs/*` websocket names and the `osjs:` mountpoint are frozen, so a front end can be replaced without changing the server.

### Terminal client

- **Four tabs.** `OVERVIEW` ranks the 5 projects with the most said in open places in the last 7 days, then lists what waits on you. `PROJECTS`, `ROOMS` and `PEOPLE` list the rest.

- **One room at a time.** The room you enter is the room you occupy, on every device. The screen becomes that room until `/exit`.

- **Every action is a command.** `/help` lists them; the [cheatsheet](#cheatsheet) groups them.

- **Reconnect.** A dropped connection logs in again with backoff, syncs, backfills and re-enters its room.

- **Bracketed paste.** A paste is text; a pasted newline or `/` line does not run.

### Testing

- **A black-box conformance suite.** `go/conformance/` tests the server against [wire-contract.md](docs/wire-contract.md) over HTTP and a websocket. `isolation_test.go` checks that it imports none of the server.

- **Agents are never in a test path.** A scripted fake reads and writes stream-json.

- **A real container test.** `make container` runs the shim in a scratch image through a bind-mounted socket.

## Status

Phase 0 of [implementation-plan.md](docs/dev/implementation-plan.md) is built on the minos side: the broker, the shim, and the broker taking the agent's pipes. Not built:

- **Grants.** Every run speaks as the one `worker` account with an ordinary session. Isolation between runs is a convention, not a rule.

- **Author kind.** A participant cannot tell an agent from a person.

- **Read windows.** `-window` is reported by `status` and enforced by nothing.

- **Submission deadlines, send rates and quotas.**

- **An end-to-end run.** No real agent has run in a container. `pma` does not yet dispatch through `sanduk`.

- **`minosa mcp`.**

[TODO.md](TODO.md) tracks the open items.

## Run

Go 1.25 or newer builds everything. There is no other toolchain.

```text
make serve # the server on http://127.0.0.1:8000
make tui   # the terminal client, in another shell
```

The accounts are `demo`, `alice`, `bob` and `worker`, each with its name as the password. Only `demo` is an administrator. Run `make tui` in a third shell to talk as a second user.

The server does not need `dist/`. If it is empty, the server warns and serves the API alone.

| Target | Does |
|-|-|
| `make go` | Build `go/minosd`, `go/minos`, `go/minosb` and `go/minosa`. |
| `make demo` | Narrate the channel audience rule, against a server and database of its own. |
| `make host-run` | Drive one real `claude` under `minosb` on the host, and check relay, held messages, both interrupt lanes and the shim. Spends turns on your account; the agent is unsandboxed. |
| `make test` | `go test -race ./...`, the wire contract included. |
| `make conformance` | The wire contract alone. |
| `make cover` | The server's coverage under the wire contract. |
| `make container` | The shim in a real container. Needs docker. |
| `make diagrams` | Render `docs/media/*.d2`. Needs d2. |

`MINOS_CONFORMANCE_CMD` names the server binary for the conformance suite; unset, the suite builds `cmd/minosd`. `MINOS_CONFORMANCE_URL` points the suite at a running server instead.

### A container run, by hand

The broker treats the run as mid-turn from the start: it pushes nothing until the agent ends a turn. So the run command must give the agent its task. In a real dispatch that command is `sanduk run`. By hand, send the task as the first stdin frame:

```sh
#!/bin/sh
# agent.sh: the task as the first frame, then whatever the broker pushes.
TASK='{"type":"user","message":{"role":"user","content":"Reply to each message in one sentence."}}'
{ printf '%s\n' "$TASK"; cat; } | exec claude -p --input-format stream-json --output-format stream-json --verbose
```

```text
MINOS_PASSWORD=worker ./go/minosb -user worker -room <id> [-channel <id>] [-interrupters demo] -socket /tmp/run/run.sock -- ./agent.sh
MINOS_SOCKET=/tmp/run/run.sock ./go/minosa messages
```

Before starting the broker:

- `demo` must invite `worker` to the room.

- Without `-channel`, `submit` is refused.

- A channel with no moderators refuses submissions. Use `/channel appoint <channel> demo`.

| `minosa` | Does |
|-|-|
| `messages [-since N] [-json]` | What was said since the last drain. |
| `say [-file F\|-stdin] [text]` | Post to the room. |
| `submit -subject S [-file F]` | Propose something that needs a decision; prints its id. |
| `await ID [-timeout SECONDS]` | Block until decided. Default 300, at most 3600. |
| `progress N` | Record that N has been read. |
| `status` | The room, the window and the connection. |

`say` and `submit` take `-payload F`, a JSON file. `--` ends the flags.

## Cheatsheet

```text
./go/minos -user alice -password alice    # log in directly; make tui prompts instead
```

`<who>` is a username, or `@name` for a group.

| Variable | Read by | Is |
|-|-|-|
| `MINOS_SERVER` | `minos`, `minosb` | The server's base URL. Default `http://127.0.0.1:8000`. |
| `MINOS_USER` | `minos`, `minosb` | The account, in place of `-user`. |
| `MINOS_PASSWORD` | `minos`, `minosb` | The password. `minos` falls back to a prompt; `minosb` requires it. |
| `MINOS_SOCKET` | `minosa` | The run's socket, in place of `-socket`. |
| `MINOS_TIMELINE_DB` | `minosd` | The database file. Default `timeline.db` under `MINOS_RUN`. |

The server's other settings are in [wire-contract.md](docs/wire-contract.md), section 1.

### Keys

| Key | Does |
|-|-|
| Tab, S-Tab (or ^N, ^P) | Next or previous tab. Not while in a room. |
| Enter | Send what is typed. On a project: open it. On a room: enter it. On a person: raise a room with them. On a channel item, with nothing typed: open or close it. |
| Up, Down | Move through a list's rows, or a channel's items. |
| PgUp, PgDn | Scroll the pane. |
| Esc | Back one level, or close archive results. |
| ^A | In a list of places: show or hide closed ones. |
| ^U | Clear the composer. |
| ^C, or ^D on an empty line | Quit. |

### Rooms

| Command | Does |
|-|-|
| `/open <who>...` | Raise an ad-hoc room, kept. |
| `/meet <who>...` | Raise a transient room. |
| `/create [project/]<title>` | Found a permanent, named room, optionally under a project. Admin. |
| `/invite <who>` | Admit a user or group. Admin only in a permanent room. |
| `/uninvite <who>` | Withdraw that grant. |
| `/exit` | Step out of this room, keeping your place in it. |
| `/leave` | Give up your place in this room. |
| `/rooms`, `/people`, `/groups`, `/projects` | List them. |
| `/close [room]`, `/reopen [room]` | Mark the work in a place done, or not. Anyone who may invite. |

A `<room>` outside the project on screen is written `project/<title>`.

### Projects

| Command | Does |
|-|-|
| `/project new <name> [#tag]...` | Found a project. Admin. |
| `/project tag\|untag <name> <tag>` | Tag or untag a project. Admin. |
| `/project file <room> [name] [task]` | File a room under a project, or none; a task sets its scope. Admin. |
| `/project rm <name>` | Dissolve a project. Its rooms remain, unfiled. Admin. |

### Channels

| Command | Does |
|-|-|
| `/subscribe <channel>`, `/unsubscribe` | Join or leave a channel's audience. |
| `subject \| body` | Typed in a channel: post with a headline. Without the bar, the first line is the headline. |
| `/channel new <title> [@group]...` | Found a channel, optionally restricted to groups. Admin. |
| `/channel admit\|revoke <channel> <group>` | Restrict a channel to a group, or lift it. Admin. |
| `/group new <name> [user]...` | Create a group. Admin. |
| `/group add\|rm <group> <user>` | Add or remove a member. Admin. |

A `<channel>` is one word: its name with `_` for each space, its id, or its id prefix as `/rooms` prints it. Typing in a channel publishes if you are an admin or moderator, and submits otherwise.

### Moderation

| Command | Does |
|-|-|
| `/channel appoint\|dismiss <channel> <user>` | Choose a channel's moderators. Admin. |
| `/queue` | Submissions waiting on this channel's moderators. |
| `/approve <id>`, `/reject <id> [why]` | Decide a submission. Moderator. `<id>` is the prefix `/queue` prints. |
| `/submissions`, `/ack <id>` | List your submissions; acknowledge a rejection. |

### Archive

| Command | Does |
|-|-|
| `/archive` | Show this space's setting. |
| `/archive <period>\|never [searchable\|private]` | Set it. Admin; permanent rooms and channels only. A period is `45`, `90s`, `30m`, `12h`, `30d` or `2w`. |
| `/archived [<after>]` | Page through the archive. Admin. |
| `/search <text>` | Search it. Admins always; others when it is searchable. |

### Other

| Command | Does |
|-|-|
| `/help` | List every command. |
| `/quit` | Leave every room and stop. |

## Layout

| Path | Contents |
|-|-|
| `go/cmd/minosd` | The server. |
| `go/cmd/minos` | The terminal client. |
| `go/cmd/minosb` | One run's broker, on the host. |
| `go/cmd/minosa` | The shim in the container. Static; imports nothing from the client. |
| `go/cmd/demo` | `make demo`. |
| `go/internal/httpapi` | Routes, the signed session cookie, the websocket upgrade. |
| `go/internal/socket` | Frame format, connection registry, fan-out. |
| `go/internal/chat` | Maps operation names to messaging calls; tracks occupancy per connection. |
| `go/internal/messaging` | The operations. Independent of transport. |
| `go/internal/timeline` | SQLite store: projects, rooms, grants, messages, submissions, sequences. |
| `go/internal/vfs` | Mountpoints, path resolution, file operations. |
| `go/internal/client` | HTTP session, socket, and protocol client with cursors and gap repair. |
| `go/internal/tui` | The terminal interface. |
| `go/internal/broker` | The six operations, delivery and tears, and the per-agent stream adapter. |
| `go/internal/link` | The run socket: framing, refusals, limits. |
| `go/internal/testserver` | A whole server in process. `Restart` reopens it on the same database. |
| `go/conformance` | The wire contract, tested against a server process. |
| `docs/` | The wire contract; design notes in `docs/dev/`; d2 sources in `docs/media/`. |
| `scripts/` | Manual probes. `probe-interrupt.sh` measures Claude Code's behaviour after an interrupt. |
| `dist/` | Served at `osjs:/`. Optional; nothing builds it. |
| `vfs/`, `.run/` | Home directories and the timeline database. Generated. |

## Documentation

| Document | Covers |
|-|-|
| [design.md](docs/dev/design.md) | Principals, trust, the workflow topology and the decisions D1 to D21. |
| [recommended-architecture.md](docs/dev/recommended-architecture.md) | The broker on the host, the shim in the container, the three lanes, and the build order. |
| [implementation-plan.md](docs/dev/implementation-plan.md) | The work per repository, its rulings, and what is built. |
| [chat-concepts.md](docs/dev/chat-concepts.md) | The model: rooms, groups, channels, read state, archival, moderation. |
| [wire-contract.md](docs/wire-contract.md) | Configuration, sessions, routes, the VFS, the socket, operations, pushes and delivery. |
| [conformance-plan.md](docs/dev/conformance-plan.md) | The conformance suite's shape. |
| [channels.md](docs/dev/channels.md) | Typed channels. An exploration; not built. |
| `agent-container-net*.md`, `*_feedback.md` | Superseded proposals and reviews, kept for their reasoning. |
| [CHANGELOG.md](CHANGELOG.md), [TODO.md](TODO.md) | Changes, and known work not done. |

## Limits

- Credentials and administrators are hard-coded in `go/internal/config`. Replace them before exposing the server.

- `MINOS_SECRET` defaults to a public development key.

- Session revocations are kept in memory, so a restart forgets a logout.

- Every account subscribes to `system`, so every user sees every other user's file paths.

- The server has one listener. Grants need separate agent-facing and human-facing listeners first ([design.md](docs/dev/design.md) 8.6).

- The database upgrades in place via `PRAGMA user_version` and never downgrades. Copy `.run/timeline.db` before running an older server.
