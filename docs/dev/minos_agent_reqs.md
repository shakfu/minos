# minos agent requirements

What `minosd` needs before a **contained** agent may use it. 2026-09-19. Nothing here is built.

[agents.md](agents.md) tests a different hypothesis: an agent is a user, in rooms with people, and the risk is that room history reaches a model provider. This document specifies the other deployment, and the threat runs the other way.

## The deployment

An agent runs in a disposable container on an internal network with no route off the host ([sanduk](https://github.com/shakfu/sanduk) `--mode sealed`). `minosd` is attached to that same network, so the agent reaches it without egress. A supervisor -- [pma](https://github.com/shakfu/pma) -- holds a session as a person, answers the agent's requests from policy, and escalates what policy declines to a human reading the same room.

Three parties, one channel:

| Party | Where | Trust |
|-|-|-|
| agent | in the container | untrusted; its input is attacker-writable |
| `pma` | on the host | trusted; mints the agent's credential |
| person | on the host | decides what `pma` will not |

## Threat direction

In agents.md the disclosure risk is outbound through the model provider, and the server cannot see it. Here the container is already sealed, so `minosd` is the only way out. That inverts the analysis:

1. **`minosd` is the containment boundary.** A person must reach it and the agent must reach it, so it is on both networks by construction. `sealed` stops meaning "nothing leaves" and starts meaning "nothing leaves except what `minosd` accepts". Every control that matters lives here.

2. **The agent's credential sits in an untrusted box.** agents.md 3.10 defers tokens until "exposure is real". A credential inside a container the agent controls is exposure, whatever the network topology.

3. **Any byte channel is an exfiltration channel.** The agent holds a source repository that must not leave. Chat is a narrow channel by accident of its 64 KiB body limit. The VFS is not.

The rule in agents.md 3.6 still governs: an agent's authority must not exceed that of the least-trusted principal who can write to it. Under this deployment that principal is the repository the agent is editing.

## What already fits

Do not rebuild these.

- **Terminal decisions.** A decided submission is refused with `That submission has been decided` (wire-contract 9). Exactly-once answering, already specified.

- **Replay after a restart.** `sync` carries the caller's own submissions, pending or rejected, oldest first. A container that died mid-wait reconnects and finds its outstanding requests without asking again.

- **Push on decision.** The `submission` push goes to the author on a decision, so an agent waits rather than polls.

- **A rejection carries a reason.** `submission.reject` takes `comment`, which maps onto `pma review --rework "feedback"`.

- **A durable progress marker.** The server-side `read` cursor never moves backwards (wire-contract 6), so "handled up to 41" survives a redeploy.

- **Drop repair.** `seq`, the cursor rule and `history` with `lastSeq` (wire-contract 8) cover a reconnect without a reliable bus.

- **A body ceiling.** 64 KiB per body under a 1 MiB frame (agents.md 3.8). No lower cap is needed for chat.

Request and reply is therefore about 80 percent built. The submissions mechanism is the right primitive; what is missing is an answer payload and a deadline.

## P0 -- without these, do not connect a contained agent

### 1. A minted grant, scoped and non-refreshable

`POST /login {username, password}` yielding a 12-hour cookie, refreshed per request to a 7-day ceiling, is wrong for this caller. It puts a reusable credential in the container and outlives the run by orders of magnitude.

Required: `pma` mints a credential per run, carrying one room, no admin group, no capability it was not given, and an expiry matching the run's timeout. It cannot be refreshed: a run that overruns loses its channel rather than extending it. `pma` can revoke it, and revocation closes every websocket on it, as `/logout` already does for a session.

Shape, respecting the route freeze (wire-contract 3): mint as a socket operation from `pma`'s own admin session, not a new HTTP route. Sections 9 to 11 set the precedent that operations extend and routes do not. The agent presents the grant as an `Authorization` header on the upgrade and on HTTP, which agents.md 3.10 already names as the available shape. A header is not a route.

A bearer token also drops what a cookie carries for nobody's benefit here: `SameSite`, the CSP `connect-src` dance, and a cookie jar written to disk inside the container.

The existing rule that a session keeps the rights it was issued with (wire-contract 2: read the role from the session, not the user table) extends to a grant unchanged. That is the property that makes this safe to build.

### 2. Capabilities, denied by default

Admin is binary today (agents.md 3.6), so "not an admin" is the only gradation available, and it is not enough. A non-admin account can still write files, enumerate people, open rooms and change settings.

Required: a grant carries an explicit capability set. Anything absent is refused with the existing error shape. For this deployment the whole set is: read one room, submit to it, and acknowledge a decision.

### 3. `/vfs` refused for a grant

`home:` is writable per user, and a `writefile` body may be 100 MiB (wire-contract 3, 4). One session covers chat and all of `/vfs`, so granting chat grants file transfer. An agent writes the repository to `home:/` and a person on the host reads it out.

Detection exists and prevention does not: a successful VFS mutation posts an event to `system` (wire-contract 7), which makes exfiltration noisy, not impossible.

Required: every `/vfs` method answers 403 for a grant. Not a size limit -- the whole namespace. An agent that needs to hand over a file writes it to the bind mount, where `pma` reads it and the container cannot reach the reader.

### 4. `/settings` refused for a grant

`POST /settings` replaces `home:/.osjs/settings.json` wholesale with any JSON object, under the 1 MiB body cap. It is a second file channel wearing different clothes.

### 5. No roster or topology disclosure

A grant must not learn what it cannot reach. `sync` returns users, rooms and groups; for a grant it returns its own room and nothing else. Rationale is reconnaissance, not privacy: the agent's output is read by a person, and a list of real accounts is material for a convincing forgery.

### 6. Separable listeners

The agent-facing address and the human-facing address must be configurable independently, so administrative operations are unreachable from the internal network at any authority level. Grant minting is reachable only from the host side.

This is the one mitigation for `minosd` being dual-homed. Without it, item 1's minting operation is reachable by the principal it exists to constrain.

## P1 -- what makes the channel work

### 7. An answer payload on approval

Approval turns a submission into a channel message authored by the submitter (wire-contract 9), so today there is nowhere to put a reply. agents.md section 6 reaches the same conclusion: "A new operation, *answer a submission*, would be required."

Required: a decision carries a payload back to the author. The cheapest form is `comment` on approve as well as reject, already pushed to the author. A distinct `submission.answer` is cleaner if approval must keep meaning publication.

### 8. A deadline, enforced by the server

An agent blocked on a decision nobody makes burns its run timeout and then dies with the work half done. The agent cannot be trusted to time itself out.

Required: a submission may carry a deadline. Past it the server decides it as `timed_out` and pushes that to the author, which is a third terminal state beside approved and rejected. A run then fails with a stated cause instead of a killed container.

### 9. Structured payloads

`body` is text. An agent's request has fields: what kind of permission, the projected cost, the path, the command. `pma` cannot apply policy to prose.

Required: a declared payload alongside the body, or a schema `pma` owns and the server passes through unread. The second keeps the schema out of the contract, which is where it belongs -- it is `pma` policy, not a wire shape.

### 10. Author kind on the message

agents.md 3.2 already proposes `kind: "user" | "agent"` on the user object.
Required here for a different reason: a person reading the room must not mistake an agent's text for `pma`'s decision. Forgery of an approval is the attack, and the agent's input is attacker-writable.

`author: "system"` stays the server's alone, which is already true and should stay stated.

### 11. A decision is an operation, not a convention

An agent lacking the capability must be unable to emit anything a reader could take for a decision. This follows from item 2, and it is worth a test of its own: a grant attempting `submission.approve` is refused.

## P2 -- record and scale

### 12. A run identifier on every object

One key joining three records: `pma`'s run row, `sanduk`'s report and relay log, and the minos archive. Without it, reconstructing what an agent did means matching timestamps across three clocks.

### 13. Retention at least as long as the relay's

A sealed run's audit trail is complete today because the relay sees every call. This channel ends that. The archive keeps the rest, so agent rooms need a retention period at least equal to the relay's log retention, or the two records cover different periods and neither is whole.

Decide which is authoritative for a dispute. Recommendation: the relay for what reached a model, the archive for what was decided.

### 14. A per-grant send rate and quota

agents.md 3.5 identifies the absent rate limit and names a per-account send rate as the only server-enforced guard. Required here as a quota too: messages per second and messages per grant. A looping agent otherwise fills the archive and the person's screen, and a compromised one drains the channel's usefulness without touching a single denied operation.

### 15. Grant lifecycle on `system`

Minted, used, revoked, expired -- announced as events, in the pattern a VFS mutation already follows. An operator who cannot see a grant being issued cannot audit item 1.

### 16. Refuse a grant in a transient room

agents.md 3.1 recommends this for history disclosure. It holds here for a second reason: a transient room promises to retain nothing, and item 13 requires the opposite. The two cannot both apply to one space.

## P3 -- efficiency

### 17. One long socket per run

Not a connection per request. The push model in wire-contract 7 and 8 already supports it.

### 18. A headless client small enough to ship in the image

`go/internal/client` is `internal`, so out-of-tree code cannot import it; `go/conformance/wire.go` is the 581-line reference an out-of-tree agent reimplements against (agents.md 4.1). A container image already carries three flavours of base, none with Node or Python guaranteed, so the client should be a static binary.

It must separate its reader from its blocking work. A connection's outbound queue holds 256 frames (`go/internal/socket/socket.go:42`) and a reader that stops to wait on a model call is disconnected (agents.md 3.9).

### 19. Backpressure refuses rather than buffers

Past the item 14 limits, the server answers with an error the agent can read. A readable refusal is already the convention (agents.md 2), and an LLM changes course on one.

### 20. No TLS on the internal network

Say so explicitly, so nobody adds a certificate lifecycle to a link that cannot leave the host. It follows that the agent-facing listener must never be the one bound to a routable interface -- item 6.

## Not required

- **Agent-to-agent messaging.** Dispatch is fan-out into isolated worktrees and review is fan-in. Two agents never need to converse. A shared room is a feature nobody has asked for.

- **Webhooks.** Rejected in agents.md 5.1, and worse here: an outbound request from `minosd` to an address an agent influenced crosses the boundary item 1 exists to hold.

- **In-process agents.** Rejected in agents.md 5.1. Also moot: the agent is in a container by requirement.

- **Streaming.** Whole replies, per agents.md 3.7. A request and a decision are not a token stream.

- **Occupancy.** Agents do not enter rooms (agents.md 3.4). Writing needs access, not occupancy.

## Conformance

`go/conformance/` talks HTTP and a websocket and imports no server code, enforced by `isolation_test.go`. Every item above is testable there, and the denials are the tests that matter: a grant gets 403 at `/vfs`, 403 at `/settings`, a refusal on `submission.approve`, an empty roster from `sync`, and nothing at all after its expiry.

Sections 9 to 11 of the contract were each a conformance test first. This should be section 13, written the same way.

## Open questions

1. Is a grant scoped to a room or to a run? A room per run matches the expiry and keeps the archive readable. A room per agent keeps history in one place and needs a grant that outlives a run.

2. Does `minosd` run as a container `pma` starts, or as an operator's service `pma` finds? The second is cleaner and means `pma` must degrade to a bind-mount mailbox when it is absent.

3. Is a capability set (item 2) general, or a single `agent` role? General costs more now and avoids a second special case later.

4. Who is accountable for a grant: the admin whose session minted it, or the run it was minted for? agents.md open question 4 asks this of accounts; a grant makes it answerable, because a grant has one issuer and one purpose.

5. Does a timed-out submission (item 8) count against the agent's failures, or against the person who did not answer?
