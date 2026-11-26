# UI review

A review of minos' user interfaces for human-to-human, human-to-agent and agent-to-agent communication. It respects the exclusions in `docs/dev/chat-concepts.md` section 6 and `docs/dev/design.md` D15; items marked **revisits an exclusion** challenge one of them.

## 1. Foundations (wire-level prerequisites)

Most UI weaknesses trace to data the wire does not carry yet.

| Gap | Why it hurts the UI | Suggestion |
|-|-|-|
| Author kind (D11, unbuilt) | A reader cannot tell an agent's text from a person's decision. `paneLines` styles only `me` and `event`. | `authorKind: user\|agent\|system` on `Message`. A fixed glyph per kind (`◆` agent, `●` person, `■` system), paired with a word, never colour alone. |
| Message kinds beyond `event` | Progress lines, permission requests and handoffs all look like speech. | `kind: say \| progress \| request \| decision \| handoff \| event`. `progress` muted, collapsed and silent; `request`/`decision` as cards. |
| Agent state on the wire (TODO High) | Clients never see `-events`; a room cannot show whether an agent is working, waiting or stopped. | `minosb` publishes a small per-room presence record: `idle`, `working (turn n, 2m)`, `waiting on you`, `blocked on permission`, `stopped(reason)`. |
| Run identity (single `worker` account) | Two runs in one room are indistinguishable. | A run label (`worker·pma#23·r2`) once per-run credentials land. |

## 2. Human to agent

1. **Agent status strip** in the room header: `◆ worker  working · turn 4 · 1m12s · 3 held · /interrupt`. Built from `pushed`, `held`, `turn`, `interrupt`, `stopped`. "3 held" tells the person their message is queued until the turn ends.
2. **Delivery state for messages to an agent**: `sent → held → delivered (turn 5)`. A short dim suffix, not per-message receipts.
3. **Interrupt as a first-class action**: a key that pre-fills `/interrupt `, with "correction is pushed when the turn stops", and a system line tying the stopped turn to the correction.
4. **Permission and decision cards**: a `request` renders as a boxed card with tool, arguments and `[a]llow [d]eny`, decided in place. Submissions likewise in the room, so a person need not visit `/queue` and copy id prefixes. Cards keep their outcome ("approved by demo 14:02").
5. **Collapsible progress**: per-tool-use lines folded into one row per turn (`▸ 7 tool calls · Edit src/x.rs, Bash cargo test …`).
6. **Payload rendering**: fenced JSON payloads (D18) collapsed to a summary line, expandable, and labelled unverified agent data.
7. **Trust cue**: agent text must never look like a system fact or a decision. No bold or system style for agents.

## 3. Agent to agent, and watching it

1. **Handoff messages as a distinct kind** (D19, D20), drawn as `── handoff: stage 1 → stage 2 ──`, with what the successor's `since` window excludes greyed and labelled.
2. **Per-agent window and read markers** in the log ("worker read up to here"): what did the agent actually see?
3. **Loop and rate visibility** (D12): send rate and quota in the status strip; warn when two agents alternate N times with no human between.
4. **Addressing without threads**: `@name` parsed into a `to:` field; clients highlight what is addressed to you; agents may answer only when addressed (a hint; D12 keeps the server rate).
5. **Machine-readable lane**: `minosa messages -json` carries kind, author kind and `to:`, so agents parse structure rather than text and system/human fields are structurally separate.

## 4. Human to human

1. **Mentions and notifications**: `@you` and `@group` raise a room in OVERVIEW's "waits on you" with an unread-mention count.
2. **Quote-reply instead of threads**: `/quote <seq>` or `r` on a selected message inserts `> 14:02 alice: …` with a back-reference.
3. **A selectable message cursor in rooms**, as feeds already have: quote, copy, show payload, jump to submission.
4. **Day separators and a "new since you left" rule** at the server-kept read cursor.
5. **Group consecutive messages** by one author within about five minutes under one header.
6. **Presence**: recently active and idle in PEOPLE, beside "here now".

## 5. TUI interaction and layout

- **Command palette with fuzzy completion** (`^K`). Tab in the composer completes commands, rooms, people, channels and submission ids, removing most id-prefix friction.
- **`?` key overlay** beside the context-sensitive footer.
- **Split layout at ≥120 columns**: rooms and agent status | conversation | inspector (payload, submission state, run events). Single pane when narrow.
- **Quick switcher** (`^G`) ranked by unread and recency.
- **Multi-line composer** (Alt-Enter newline) and a subject/body form in channels in place of `subject | body`.
- **Confirmations** for authority and destructive actions, as the existing `pending` y/n prompt does for leaving.
- **Inline errors with a fix**: refusals flashed by the composer with the command that resolves them.
- **Accessibility**: never colour alone; honour `NO_COLOR`; high-contrast and 16-colour themes; every action reachable by key and by command.
- **Mouse** (optional; tcell supports it).

## 6. A web or desktop client

The OS.js wire format is frozen and `dist/` is served but empty. A small web client on the existing websocket suits: side-by-side room and inspector, agent turn timelines, diff previews for permission requests and artifacts (via VFS), browser notifications for "waits on you", and a cross-room supervision dashboard of every live run (pma's `H`). Keep the TUI primary; share the `client` package's semantics.

## 7. Revisiting exclusions (optional)

- **Streaming (D15), revisits an exclusion.** Keep whole replies; add an ephemeral, unsequenced presence field for live progress. It consumes no `seq` and touches no read cursor.
- **Reactions, revisits an exclusion.** A single acknowledgment (`/ack <seq>`, shown `✓ alice`) gives a no-noise "seen, handled" and an unambiguous signal to agents.
- **Threads.** Keep excluded; quote-reply and `to:` cover most of the need.

## 8. Order

1. Author kind and message kinds on the wire, and their rendering.
2. Agent status strip and held/delivered state, from agent state on the wire.
3. Request and submission cards, decided in place.
4. Command palette, Tab completion, quick switcher.
5. Collapsible progress, "new since you left", consecutive-message grouping.
6. Mentions, `to:`, quote-reply, message cursor in rooms.
7. Window and handoff visualisation (after grants).
8. The web supervision dashboard.

## 9. Status

Items 1 to 3 are implemented; see CHANGELOG.md.
