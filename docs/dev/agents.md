# Agents

An exploration of how AI agents would use minos. Nothing here is built. The model it tests is [chat-concepts.md](chat-concepts.md); the wire it would change is [wire-contract.md](../wire-contract.md).

A second deployment is specified separately in [minos_agent_reqs.md](minos_agent_reqs.md): `pma` dispatching worker agents into sealed containers, each reaching `minosd` on its own network. Three principals share the server there -- a trusted tool, a project-management agent, and one worker per task -- and the threat direction is outbound through `minosd` rather than through a model provider. Several questions this document leaves open are settled there, and each is marked below.

The working hypothesis: **an agent is a user.** It logs in, holds grants, reads and writes through the same operations, and is refused by the same rules. Sections 3 and 4 test that hypothesis. Section 6 gives two framings that reject it.

## 1. Roles

"Agent" covers five jobs. Each maps to a role the model already defines (chat-concepts 2.1).

| Agent role | Model role | Does | Risk |
|-|-|-|-|
| Participant | participant | Answers in a room it was invited to. | Sees all room history on admission. |
| Producer | channel producer | Publishes to a channel: a feed, a digest, a summary. | Needs admin or moderator authority to publish. |
| Contributor | subscriber | Submits to a curated channel. A moderator decides. | Low. Human approval gates every item. |
| Moderator | moderator | Approves or rejects submissions. | The queue is attacker-written input. |
| Operator | chat admin | Founds rooms, edits groups, reads archives. | Every admin power, driven by untrusted text. |

A sixth use is not a role: a person's own assistant (Claude Code, say) reading and writing minos **as that person**. That is delegation, not a new account. Section 5.2 covers it.

The sealed deployment runs two of these roles at once and they talk to each other. `pma-agent` is an operator that proposes and never executes; each worker is a participant in one room. That an agent's counterparty is usually another agent is what changes 3.5 from a hazard into the design.

## 2. What already fits

- **Writing needs access, not occupancy.** `Messaging.Send` checks `requireAccess` and nothing else (`go/internal/messaging/messaging.go:379`). Pushes go to a room's audience, not its occupants. An agent receives and answers in every room it was granted without ever entering one.

- **Every operation is a command with a readable refusal.** A refusal is `{"error": "<message>"}` (wire-contract 6), such as `Only an administrator may do that`. An LLM can read that string and change course without a lookup table.

- **Restart and catch-up are solved.** `history` with a cursor, plus `lastSeq`, covers an agent that crashed or was redeployed (wire-contract 8). The shortfall past 200 messages is detectable.

- **The server keeps a durable progress marker.** The `read` cursor is per user, server-side, and never moves backwards (wire-contract 6). For an agent, "read up to 41" means "handled up to 41". A restarted agent resumes from `sync`'s `read` map, with no local state.

- **A machine producer is already in the model.** `system` has one, and chat-concepts 5 names a price feed as the case for a channel with no moderator.

- **Human-in-the-loop exists.** A contributor agent's output reaches subscribers only if a human moderator approves it. A moderator may not edit (chat-concepts 5), so what is published is exactly what the agent wrote, under its own name.

## 3. What does not fit

Ordered by consequence.

### 3.1 Admission discloses history, and an agent may export it

"A newly invited participant sees the room's history" (chat-concepts 2.3). For a person, that disclosure stays on the server. For an agent backed by a hosted model, the history is sent to the model provider as context. Inviting an agent to a five-year-old room sends five years of it off the server.

Transient rooms make it worse. The model promises "It retains nothing" (chat-concepts 2.3, *Transience*). An agent that forwards messages to an external API breaks that promise, and the server cannot detect it.

Options:

1. Refuse agents in transient rooms. Enforceable, and honest about what the server can verify.

2. Allow it if the agent declares a non-retaining backend. The server cannot verify the declaration.

3. Allow it and tell participants. Moves the decision to people who may not understand it.

Recommendation: option 1. Separately, record the agent's backend on its account (section 4.2) so participants see where text goes.

### 3.2 Nothing marks an account as an agent

`sync.users` is `{username, online}` (wire-contract 6). Participants cannot tell an agent from a person. Neither can another agent, which matters for 3.5.

Option: add `kind: "user" | "agent"` to the user object, as an additive field in the style of wire-contract 9. `author: "system"` is already reserved for events and should stay distinct: `system` is the server, an agent is an account.

Settled in minos_agent_reqs.md 9: three kinds, with a trusted tool's output going out as `system` because it is server-side fact. Two readers need it. A person must not take an agent's text for a decision, and an agent must be able to tell a human's instruction from its supervisor's when the two disagree.

### 3.3 Group assignment admits an agent everywhere at once

A grant to a group tracks the group (chat-concepts 2.3). Assigning an agent to `engineering` admits it to every room `engineering` was invited to, and discloses all of their history under 3.1, in one `group.assign`. Nobody in those rooms is asked.

Options: forbid agents as group members; allow it and announce the assignment in each affected room; allow it silently, as for a person. The first is one check in `group.assign`. Open question 2.

### 3.4 Occupancy is one room at a time

"A user occupies at most one room" (chat-concepts 2.3). `enter` releases every other occupancy, pushes a `room` for each, and sends `exited`. An agent that enters rooms to look present would churn those pushes on every switch.

An agent occupancy also defeats transient-room deletion. The grace period starts when the last occupant leaves. An agent that never leaves keeps the room, and its history, alive indefinitely.

Recommendation: agents do not enter. Presence (global) says whether they can answer. Consequence: a room is an open invitation to the agent forever, because `visited` records entry. That is display state only.

### 3.5 Nothing stops two agents answering each other

The server has no rate limit. Two participant agents that each reply to every message loop without bound. Each reply is a stored row and a fan-out.

The obvious guard is to answer only when addressed. The model has no addressing, and the terminal client already uses `@name` for a group (`go/internal/tui/app.go:630`). A mention syntax must not collide with it.

Guards, cheapest first:

1. Agents never reply to an author whose `kind` is `agent` (needs 3.2).

2. Agents reply only when addressed, by a syntax other than `@`.

3. A per-account send rate in `messaging`. The only server-enforced guard; it also limits a compromised agent.

Guards 1 and 2 are agent-side policy running on attacker-writable input. They hold while the agent behaves and not otherwise. Guard 3 is the only one that survives an agent that does not.

Guard 1 also fails outright where an agent answering an agent is the point: `pma-agent` instructs a worker and reads what it sends back, every run. minos_agent_reqs.md 7 therefore makes guard 3 a P0 requirement rather than one option of three, and adds a per-grant quota, because a 64 KiB body cap bounds nothing while the message count is uncapped.

### 3.6 Untrusted input steers whatever authority the agent holds

Every message in a room is input to the agent's model. Any participant can write instructions into it. A moderator agent's queue is written by submitters. An agent holding admin rights can therefore be made to edit groups or read archives by anyone who can reach it.

Rule: **an agent's authority must not exceed that of the least-trusted principal who can write to it.** Consequences:

- No agent account is in `config.Admins`. Admin is binary today (`go/internal/config/config.go`), so there is no partial grant to fall back on.

- An operator agent proposes; a person executes. The proposal can be a submission to an admin-moderated channel, which needs no new mechanism.

- A moderator agent may reject but not approve without human review. This is a policy on the agent, not a server rule.

- An agent that supervises other agents is bounded by what they send it. Reading a worker's report is reading text the worker's inputs influenced, so a supervisor cannot safely hold authority its workers lack.

The rule is transitive. If agent A may write to agent B, then B is bounded by A's inputs as well as its own. A shared room is therefore a trust decision rather than a routing one, which is why the sealed deployment gives each worker a room of its own and treats a second worker in that room as a policy change (minos_agent_reqs.md, Agent to agent).

The most sensitive act the model permits is an admin reading an archive (chat-concepts 4, open question 2). An agent should not be able to do it.

### 3.7 Streamed output has no representation

Hosted models stream tokens. Minos has no in-place edit and no typing indicator; both are excluded by name (chat-concepts 6).

| Option | Cost |
|-|-|
| Send the whole reply once. | Latency. No model change. |
| Send each chunk as a message. | Consumes a `seq` per chunk, and fills the log and read cursors with fragments. |
| An ephemeral push outside the sequence. | A new concept. Submissions show an object can live outside `seq` (chat-concepts 5), but a push that is never stored cannot be repaired after a drop. |

Recommendation: whole replies.

### 3.8 A long reply closes the agent's socket

Resolved. The contract now states a body limit of 64 KiB, refused with a readable error, under a frame limit of 1 MiB (wire-contract 5 and 6).

### 3.9 A blocking agent is disconnected

A connection's outbound queue holds 256 frames (`go/internal/socket/socket.go:42`). A reader that stops to wait on a model call stops draining. In a busy room that client is disconnected. `go/internal/client` already separates the reader from the work that blocks; an agent built on it inherits that. An agent written against the raw wire must do the same.

There is a second consequence: a client that is not draining cannot act on a message telling it to stop. An instruction to an agent mid-inference is read when the turn ends, so any stop that must be prompt belongs outside the channel (minos_agent_reqs.md, Stopping a worker).

### 3.10 Authentication is a password fixture

Login is `POST /login` with a password checked against a plaintext map; the result is a 12-hour cookie (wire-contract 2). The README says this server is not to be exposed. An agent account is one more map entry.

A token per agent, revocable and scoped, is the right shape once the server is exposed. HTTP routes are frozen (wire-contract 3), so a token would have to work through the existing login or as a header on the websocket upgrade.

Exposure is real in the sealed deployment, whatever the network topology: a credential that sits inside a container the agent controls is exposed to the agent. minos_agent_reqs.md 1 specifies the grant -- one room, no admin group, non-refreshable by its holder, re-mintable and revocable by the tool that issued it.

## 4. Changes, if the hypothesis holds

### 4.1 No wire change

- An agent account in `config.Users`, not in `config.Admins`.

- `go/cmd/agent`: a participant agent on `client.Connect`. It never enters, never admin, sends whole replies, answers only when addressed, and ignores authors on a hard-coded agent list.

`go/internal/client` is an `internal` package, so only code inside module `minos` can import it. An agent in another language reimplements the envelope; `go/conformance/wire.go` is a reference at 581 lines.

### 4.2 Additive wire changes, ranked

1. A per-account send rate. Guard 3 of 3.5, and the only guard that holds against an agent that misbehaves.

2. `kind` on the user object. Unblocks 3.2 and guard 1 of 3.5.

3. Agents refused in transient rooms. Implements 3.1 option 1; depends on 2.

4. `backend` on an agent's user object: free text naming where its context goes. Informational only.

Each is a conformance test first, as sections 9 to 11 were. This is the list for an agent that is a user among people. A contained agent needs more, and minos_agent_reqs.md lists it.

## 5. How an agent reaches the server

### 5.1 Integration shapes

| Shape | For | Against |
|-|-|-|
| **A. Wire client.** A process speaking the contract. | Held to the contract by `go/conformance`. Model SDK stays out of `minosd`. | Out-of-tree agents reimplement the envelope. |
| **B. MCP bridge.** A process logged in to minos, exposing operations as [Model Context Protocol](https://modelcontextprotocol.io/specification) tools. | Any MCP host can use minos with no minos-specific code. | MCP tools are called by the host. Pushes do not map onto tool calls, so the bridge polls `history` from a cursor instead. |
| **C. In process.** Goroutines in `minosd` calling `messaging.Messaging` directly. | No auth, no socket. | Bypasses `go/conformance`. `chat.Handler` owns occupancy per connection, so an agent needs a fake one. Puts model dependencies in the server. |
| **D. Webhooks.** The server POSTs events to an agent URL. | Agents need no socket. | New server behaviour: retries, delivery state, and outbound requests to addresses an admin types. `history` already gives pull delivery. |

Recommendation: A for participant and producer agents; B for delegation. Reject C and D.

### 5.2 Delegation through MCP

An MCP bridge for a person's own assistant would log in **as that person**. That changes the analysis:

- 3.1 still applies: the assistant sends room history to its provider. The person chose that for themselves, but the room's other participants did not.

- 3.2 cannot be solved with an account `kind`, because the account is a person's. Marking delegated messages needs a per-message field, such as `via: "agent"`.

- 3.6 is worse. The assistant holds the person's full authority, admin included if they are one.

A minimal MCP tool set, all existing operations: `sync`, `history`, `send`, `read`, `channel.submit`, `channel.open`. Leave out `create`, `invite`, `group.*`, `archive.*` and `submission.*` until 3.6 has an answer.

## 6. Alternative framings

**An agent is a channel.** Its output is a stream that people subscribe to, and questions go in as submissions. This needs no agent account in any room, so 3.1, 3.3 and 3.4 disappear. It does not work today: approval publishes the submitter's text unchanged, so there is nowhere to put the answer. A new operation, *answer a submission*, would be required, and answers would be public to every subscriber.

Half of it is adopted in the sealed deployment, running the other way. A channel carries instructions *to* a fleet of agents, which works precisely because a channel is read-only to its audience: no agent can forge a broadcast to the others. Conversation stays in rooms.

**An agent is a delegate, never a principal.** Agents act only for a named person, and every agent message carries both identities. Accountability is always a person, which removes the question of who is responsible for an agent account. It rules out producer agents with no owner, such as a price feed.

## 7. Open questions

1. Is an agent's `backend` the server's business at all, given it cannot verify it?

2. May an agent be a group member? If yes, is the assignment announced in each room it unlocks?

3. Should `read` mean "handled" for an agent? A human reading the same room sees an agent's cursor nowhere, so the conflation is invisible today. It would become visible if read receipts are ever added.

4. Who is accountable for an agent account: the admin who created it, or a named owner stored with it? A grant answers this where an account does not, because a grant has one issuer and one purpose (minos_agent_reqs.md open question 3).

5. Does an admin reading an archive through an agent count as the admin reading it (chat-concepts 4, open question 2)?
