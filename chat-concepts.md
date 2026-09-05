# Chat concepts

The model of conversation for minos, defined before and independently of any
way of reaching it. Nothing here is about windows, panes, tabs or terminals. If
a concept in this document can only be explained by describing a gesture, it is
not yet a concept.

Section 2 is the model. Section 4 is what it does not yet say and must. Section
6 is what it costs in the existing code, which implements a different model —
one where a room *was* its membership. That idea is retired here: rooms are
places, membership moved out into groups, and admission is by invitation.

## 1. Method

Three questions decide what any of these objects is. They are worth asking
explicitly because the answers are not obvious, and because the model they
replace answered them by accident.

**Identity** — what makes this the same object tomorrow? A name, an opaque id,
or the set of people in it? Identity is not a field; it is the rule for deciding
sameness, and everything else depends on it.

**Lifetime** — when does it begin, and what ends it? Specifically: if everyone
leaves, does it still exist?

**Authority** — who may change it, and what happens to what came before when
they do? Admitting a person to a five-year-old room is either a small act or a
disclosure of five years of history, and the model must say which.

Two distinctions run through everything below and are easy to lose:

- **Access is not occupancy.** Who *may* be in a room and who *is* in it are
  different facts. A permanent room has participants who are usually absent; a
  transient room exists only while occupied. Without this split the lifetime of
  a transient room cannot be stated at all, because membership does not end —
  people do not resign from a conversation, they stop being in it.
- **Invitation is not subscription.** Rooms are closed: you are in one because
  someone with the authority to invite you did. Channels are open: you subscribe
  because you are interested. Different acts, by different people, revoked for
  different reasons.

## 2. The model

### 2.1 Users and roles

A **user** is an account that can author, read, moderate, and be assigned to
groups. Referenced by a stable id rather than a display name, so authorship
survives a rename and no username can impersonate the machine. The roster of who
exists is the host's business; `messaging/` already treats it as an injected
`roster()` and should continue to.

Roles are not a hierarchy. They are separate authorities over different objects,
and a user may hold several.

| Role | Scope | Confers |
|-|-|-|
| **Chat admin** | system | Creates permanent rooms and channels, and is the only one who may invite to a permanent room. Assigns group membership. |
| **Owner** | one ad-hoc room | Created it. |
| **Moderator** | one channel | Publishes directly; approves or rejects submissions. |
| **Participant** | one room | Has access, by invitation. Reads and writes. In an ad-hoc room, may also invite. |
| **Occupant** | one room, right now | Is present. Distinct from participant. |
| **Subscriber** | one channel | Receives what is published. By own choice. |

The important line is between **chat admin** and **ordinary user**, and it runs
through creation and invitation together:

> Permanent rooms are created and populated by admins. Ad-hoc rooms are created
> and populated by whoever wants one.

Permanent rooms are institutional: they represent an ongoing concern that
outlives any individual's interest, so who is in one is an administrative fact
and a participant cannot change it. Ad-hoc rooms are personal: any user raises
one by addressing people, and any participant may bring in another. That
permissiveness is not a concession — restricting invitation to the owner would
buy nothing, since a participant who wanted to add someone could simply raise a
new room with everyone in it. Groups are administrative for the same reason
permanent rooms are: both state how the organisation is arranged, rather than
what someone felt like doing this afternoon.

### 2.2 Group

**A lasting set of users.** A user may be assigned to more than one group.

| | |
|-|-|
| Identity | An opaque id with a name. Not its member set — two groups may have identical members. |
| Lifetime | Lasting. Founded deliberately, outlives any particular member. Emptying a group does not delete it. |
| Authority | Assignment is administrative: the chat admin grants and revokes. A user does not join a group by wanting to. |

A group is **a principal, not a conversation.** It exists to be named where
access is decided, so admission can be granted to a set of people once rather
than to each of them repeatedly, and so a new user inherits access by being
assigned rather than by being invited to everything individually.

The load-bearing consequence: **groups have no message log.** If a group had its
own conversation it would be a permanent room whose access rule is itself, and
the two concepts would collapse. A group that wants to talk gets a room.

### 2.3 Room

**A place where a chat or a meeting happens.** Identity is the room itself, not
the people in it. Adding or removing a participant leaves the same room; two
rooms may have identical participants; a room everyone leaves is empty rather
than gone — except where its type says otherwise.

#### Admission

**All rooms are by invitation.** There is no self-join, no directory to browse,
no discoverable room. A user is a participant because someone invited them.

Invitation grants access; it is not an offer awaiting acceptance. This is what
keeps the access/occupancy split clean:

> Invitation makes you a **participant**. Entering makes you an **occupant**.

A participant who never enters is still a participant. An occupant who leaves is
still a participant — unless the room was transient, in which case there is
shortly nothing left to be a participant of.

**An invitation names either a user or a group.** Inviting a group is the whole
reason groups exist, and the grant *tracks* the group rather than expanding to a
list of names at the moment it is issued: a user later assigned to the group
gains access without a second invitation, and a user removed from it loses
access. That second half is the consequence worth being deliberate about — a
single change to a group assignment silently revokes access to every room the
group was invited to, which is exactly what makes groups worth having and
exactly what makes them dangerous to edit casually.

#### (1) Permanent

**Created by the chat admin, who is also the only one who may invite to it.**
Founded deliberately and named. Persists indefinitely, independent of whether
anyone is in it. Its history belongs to the room, so it survives every change of
participant.

This is the room an ongoing concern lives in — a team's room, a project's room.

**A newly invited participant sees the room's prior history.** The history
belongs to the place, not to the people who happened to be present when it was
written, so admission is admission to all of it. There is no per-user bound and
no invited-since fact to store: every participant of a room sees exactly the
same thing.

What varies is not who may see the history but how much of it exists, and that
is what archival is for.

#### (2a) Ad-hoc, persisted

**Created by any user**, without ceremony, by addressing people, and persisted
from the outset. Any participant may invite others. Its history is durable, and
after creation it behaves largely as a permanent room does.

#### (2b) Ad-hoc, transient

Created the same way, and **not persisted**. It exists for the duration of the
conversation and is deleted when that ends, history included.

**A transient room retains nothing.** Not on deletion, and not by any later
change of mind: there is no conversion that rescues what was said. This is the
meeting-room analogue in the strict sense — the room is released when the
meeting is over and nothing is kept — and the guarantee is unconditional, which
is the point. A promise of discard that someone with the right button can
retroactively withdraw is not a promise. Whether persistence is wanted is
therefore decided when the room is raised, by the person raising it, and never
again.

**It ends when the last occupant leaves, after a grace period.** The grace
period is not a detail — without it, a dropped connection, a page reload or a
closed laptop lid destroys a live conversation. The sequence is: occupancy falls
to zero, the room records the moment it emptied, and it is deleted once the
grace expires. Anyone re-entering before then cancels the deletion.

The duration is a genuine tension rather than a tuning exercise. Too short and
an accidental disconnect loses the conversation. Too long and the room lingers
after the participants believed it was gone — and deletion here is a promise
made to the people who spoke in it, not merely a retention policy. Somewhere
around two minutes is the right order of magnitude; it should be configurable,
and it should be documented to users, because they are relying on it.

Two mechanical consequences:

- **Emptiness must be stored, not timed in memory.** The room carries the moment
  its occupancy fell to zero, so any worker can decide whether the grace has
  expired. An in-process timer would be lost on restart and invisible to a
  second worker, and this server already runs several.
- **A restart ends every transient room.** Occupancy derives from live
  connections, so a restart empties all of them at once. This wants exactly the
  sweep the codebase already has: `Timeline.sweep_dead_workers` reclaims presence
  rows abandoned by a worker that died, using a POSIX lock as the liveness
  signal. Orphaned transient rooms are the same problem and should be reclaimed
  by the same pass.

**"Not persisted" does not mean "never stored."** Sequence numbers are assigned
inside a database transaction precisely because several workers append
concurrently (`messaging/timeline.py`, `BEGIN IMMEDIATE`); an in-memory room
cannot be sequenced safely across workers, and without sequencing the client's
gap repair has nothing to work with. A transient room is therefore stored like
any other and **deleted** when it ends. Transience is a retention rule, not a
storage strategy — and the rule has to be enforced by something that runs even
when nobody is watching, which is the sweep above.

#### Archival

**An admin may set an archival frequency on a room they create.** At each
interval the room's accumulated history is archived and leaves the live room.

The essential property, and the reason this is a clean answer to the visibility
question: **archival bounds what exists, not what a newcomer may see.** A
participant admitted yesterday and one admitted five years ago see the same
messages — the ones since the last archival. Retention is uniform, and the model
needs no per-user history rules at all.

A frequency is therefore a statement about how long a room's conversation stays
live, chosen when the room is founded by the person accountable for it. A room
with no frequency set keeps everything.

**Archived history is stored, not destroyed, and the chat admin can read it.**
It leaves the live room and is beyond every participant's reach, including the
people who wrote it; an admin retains a path to it.

That makes archival the opposite kind of promise from a transient room, and the
two must never be described to users with the same word:

| | Transient room | Archived history |
|-|-|-|
| What is retained | nothing | everything |
| Reachable by participants | nothing exists | no |
| Reachable by the chat admin | nothing exists | yes |

A transient room's contents are gone in the sense people mean when they ask.
Archived history is not gone and not private; it is merely out of *their* reach.
Saying "deleted" for the second would be false, and false in the direction that
matters, so the interface has to be careful here whatever it looks like. If
users are told anything at all about archival, the honest sentence includes the
admin.

##### Where it lives

**The same database, in its own table.** Archived rows move from `messages` to
an `archived_messages` table beside it, keyed by `(room_id, seq)` as the live
table is. A row is archived unchanged — author, kind, body, timestamp and above
all its sequence number — so the archive is the room's history rather than a
rendering of it.

The point of the move is to keep the *hot table* small, and a separate table
achieves that completely: the live queries are all indexed on `(room_id, seq)`,
and both the backfill and the sequence lookup ride that index, so what matters
is how many rows the live table holds rather than how large the file is.

Keeping it in one file is what makes the move atomic. A transaction spanning two
attached SQLite databases is *not* atomic when the main database is in WAL mode,
and `Timeline.init` sets `PRAGMA journal_mode=WAL`. The failure is quiet: the
cross-database commit succeeds, returns no error and raises no warning — it
simply stops guaranteeing that both halves land. A crash mid-commit could then
delete rows from the live room without landing them in the archive, losing
precisely the messages the feature exists to keep. In one file the whole move is
a single `BEGIN IMMEDIATE`, which `Timeline._write` already provides.

Two rules make the split into a separate file a contained change later, and both
are worth following now because they are cheap:

- **The room stores its own high-water mark** rather than deriving it. Required
  for correctness in any case (see below), and the one thing that would
  otherwise silently depend on the archive being co-located.
- **The archive is keyed by `(room_id, seq)`**, so moving a row twice is a
  no-op and any retry is safe.

With those in place, moving the archive to its own file is a local change, and
it should be made on evidence rather than in advance: the archive running an
order of magnitude ahead of the live table, backup or checkpoint windows growing
painful, or a wish to put the archive on different storage under its own
retention. The cost at that point is giving up the single transaction and
replacing it with a two-phase move — archive, commit, then delete — which the
idempotent key above makes safe.

**Not in the VFS, either way.** A file is the obvious home for an export, but
the VFS is user-visible by construction and this material is defined by not
being reachable by users. It belongs beside the timeline, where no mountpoint
resolves.

##### How an admin reads it

Not through `history()`. The live read is deliberately capped at the tail and
deliberately not pageable — "asking again from the same cursor returns the same
slice" — because its job is to repair a client's gap, and a client that is
further behind than the cap is told so rather than walked backwards through the
room. An admin reading an archive wants the opposite: a bounded, ordered,
pageable walk over material that is large by definition and not latency
sensitive. It is a separate operation with a separate authority check, and
trying to reuse the live one would compromise both.

Two constraints on how this is built, both of which come from the delivery
design rather than from taste:

- **Sequence numbers must never restart.** They are per-room, monotonic and
  contiguous, and every client holds a cursor into them. If archival reset the
  count, a client holding cursor 500 would receive a new message numbered 1,
  judge it already seen, and silently discard it — the message would never
  appear, with nothing anywhere reporting a fault. So archival removes messages
  from the low end of the sequence and never renumbers what remains.

  This has a specific consequence for the store. `Timeline._last_seq` derives
  the next number from `MAX(seq)` over the messages actually present
  (`messaging/timeline.py`), which is correct only while some remain. A room
  archived down to empty would report zero and begin again at one, colliding
  with numbers already issued. **The room must carry its own high-water mark**,
  independent of its rows, and report that as `lastSeq`. Reading it across the
  live and archived tables together would also be correct while both sit in one
  database, but it would make every append depend on that arrangement holding —
  which is exactly the dependency the storage note above is trying not to
  create.

- **The client already handles the resulting gap.** A participant returning with
  a cursor below what the room still holds is precisely the case
  `client/src/core/chat.ts` was built for: the backfill starts above the cursor,
  the shortfall is detected against `lastSeq`, and the log says `N earlier
  message(s) not shown` rather than pretending. Archival is a second producer of
  a condition the client can already describe, so it needs no new protocol —
  provided the high-water mark above is honest.

Archival, like the expiry of a transient room, must run whether or not anyone is
connected. It belongs to the same sweep.

### 2.4 Channel

**A curated, read-only information stream.** A channel is not a conversation.
Its distinguishing property is that the right to publish and the right to read
are held by different people.

| | |
|-|-|
| Identity | An opaque id with a name. |
| Lifetime | Lasting, like a group. Independent of subscribers. |
| Authority | Subscribers read. Moderators publish, and decide what else is published. |

Channels are the one open object in the model. Where a room is closed and
entered by invitation, a channel is subscribed to by choice — the asymmetry is
deliberate, and it is the difference between a conversation and a broadcast.

For a machine channel the producer is whatever generates the stream. The
existing `system` stream is exactly this: a channel with a machine producer, no
submissions, and every user subscribed.

#### Submission

A user may send a message to a channel. **It is not broadcast.** It becomes a
submission, visible to the channel's moderators, and it reaches subscribers only
if a moderator approves it.

The mechanism this requires is not the message log, and the reason is precise.
Per-channel sequence numbers are contiguous, and a client treats a gap as
evidence of loss and asks for a backfill that closes it
(`client/src/core/chat.ts`). If a submission consumed a sequence number and was
then rejected, every subscriber would see a permanent hole and re-request it
forever. **A submission therefore lives outside the channel's sequence** and is
assigned one only on approval, at which point it becomes an ordinary published
message.

So a submission is a distinct object with its own lifecycle — submitted, then
approved or rejected — and the channel's log contains only what was published.

### 2.5 Read state

Per user, per room or channel: the highest sequence that user has read. It
belongs on the server, not in a front end, because it is the same fact from
every device and every interface.

This is a **second** cursor, distinct from the delivery cursor that already
exists in `client/src/core/chat.ts`. The delivery cursor answers "what have I
received" and exists to repair gaps. The read cursor answers "what has this
person seen". Conflating them marks a message read by arriving, which is wrong
on any interface and conspicuous on one that shows unread counts.

A transient room needs no read state, since nothing survives to be unread.

## 3. The model in one table

| | **Group** | **Permanent room** | **Ad-hoc persisted** | **Ad-hoc transient** | **Channel** |
|-|-|-|-|-|-|
| Created by | chat admin | chat admin | any user | any user | chat admin |
| Who may admit | chat admin | chat admin only | any participant | any participant | nobody — self-subscribe |
| Admission is | assignment | invitation | invitation | invitation | subscription, by choice |
| Invitation names | — | user or group | user or group | user or group | — |
| Identity | opaque id | opaque id | opaque id | opaque id | opaque id |
| Named | yes | yes | yes | derived from occupants | yes |
| Has a message log | **no** | yes | yes | yes, discarded | yes, published only |
| Who may write | — | participants | participants | occupants | moderators; others by submission |
| Who may read | — | participants | participants | occupants | subscribers |
| Ends when | deleted deliberately | deleted deliberately | deleted deliberately | **grace expires after last occupant leaves** | deleted deliberately |
| Survives being empty | yes | yes | yes | **no** | yes |
| Retains history | — | yes | yes | **never** | published only |
| Live history bounded by | — | archival frequency, if set | not yet decided | n/a | not yet decided |
| Archived material | — | stored; admin-readable | n/a | none exists | n/a |
| New participant sees | — | all live history | all live history | n/a | all live history |

## 4. Decisions still required

Genuine forks. Each changes what gets built.

1. **Does the archive itself ever expire?** Nothing currently removes it, so an
   archived room grows without bound and the store holds every message ever
   sent, forever. That may be intended. If it is not, the limit belongs in the
   model rather than arriving later as an operational surprise — and it is the
   one place where the word "deleted" would finally be accurate.

2. **Is an admin's read of an archive recorded?** Reading archived history is
   reading conversations whose participants can no longer reach them, which is
   the most sensitive act the model permits. This project already has the
   natural place to announce it: the `system` channel carries machine events,
   and an archive read is exactly that kind of event. Announcing it is a choice
   rather than an obligation, but making it silently is also a choice.

3. **What are the frequency units, and what is the default?** Monthly or
   quarterly are the plausible shapes. More consequential is whether "never" is
   the default for an admin who sets nothing, which is the assumption in 2.3 and
   the safe one, since the alternative silently moves history out of reach that
   nobody chose to lose.

4. **Do ad-hoc persisted rooms get retention at all?** Only admin-created rooms
   carry an archival frequency, so a user-raised persisted room keeps everything
   live forever and no one is accountable for it. That may be right — it is the
   user's own room — but it is the asymmetry most likely to be regretted,
   because it is where the volume actually accumulates.

5. **Who may delete a room, and can one be handed over?** Creation authority
   separates permanent from ad-hoc-persisted, but nothing yet says whether an
   admin may delete a user's persisted room, whether an owner may delete one
   others are actively using, or whether ownership transfers when its owner
   leaves the organisation. Absent an answer, "owner" is only provenance. Note
   that deleting a room with an archive raises question 1 again from a different
   direction.

6. **What does a rejected submission tell its author?** Silence, a bare
   rejection, or a reason. Relatedly: may a moderator edit a submission before
   publishing — "curated" suggests yes — and is a published submission
   attributed to its author or to the moderator who approved it?

7. **Who appoints moderators, and may a channel have none?** Presumably the chat
   admin, by symmetry with permanent rooms and channels. A channel with no
   moderator accepts submissions nobody can ever approve.

8. **Is channel subscription open to everyone?** Section 2.4 makes channels the
   open object, but a channel restricted to a group is an obvious want, and it
   would be a different mechanism from a room's invitation.

9. **May a participant leave a room?** Under Place semantics leaving is not a
   change to the room but to that user's relationship with it. The harder half
   is what it means when access came from a group assignment rather than a
   personal invitation: leaving would be undone the moment the grant is
   re-evaluated, so either it cannot be left, or the departure is itself a
   stored fact that overrides the group grant.

10. **Is presence global or per-room?** Today it is global — online or not,
    published to the whole roster. Occupancy needs something finer, at least for
    transient rooms, where it decides when the room dies.

11. **Terminology: "system room".** Worth settling early, because `system` is
    already the id of the machine channel (`server/config.py`, `SYSTEM_STREAM`).
    Using "system room" as a synonym for an admin-created permanent room would
    collide with it in every log line and error message. Recommend reserving
    "system" for the machine channel and calling the other kind "permanent".

## 5. Deliberately excluded

Named so their absence is a decision rather than an oversight: threading and
replies; editing and deletion of published messages; reactions; typing
indicators; attachments as first-class objects rather than VFS paths mentioned
in text; federation across servers; calls or huddles layered on a room; and any
conversion of a transient room into a persisted one.

## 6. What this changes in the code

For orientation, not as a work plan. The existing `messaging/` implements the
retired model, in which a room *was* a set of people.

**Retired.** `merge` (`messaging/service.py:210`) folded one room's membership
into another's. It was a membership-as-identity gesture and has no meaning when
a room is a place — two places do not become one. The client-side pair dedupe at
`client/src/apps/Chat.ts:175` goes with it, along with the derived room title
frozen at creation and never recomputed.

**Reworked.** `open_room` splits by type and gains a creation-authority check.
`invite` becomes the single admission mechanism, accepts a group as well as a
user, and is authority-checked per room type; grants that track a group must be
re-evaluated when assignments change, which is new behaviour rather than a
tightened check. Room `kind` stops being advisory: the service currently accepts
a `send` to a stream from any member (`service.py:166` checks membership, not
kind), which must instead be refused and routed to submission. `leave` currently
revokes access to history the leaver wrote, a side effect of `require_member`
gating `history` (`service.py:124`, `:153`); that needs revisiting alongside
decision 9.

**New.** Groups, as a stored principal with assignments. The chat admin role.
Room type, the stored empty-since moment, and the sweep that deletes transient
rooms once grace expires. Occupancy, distinct from access. Channel subscriptions
and moderators. The submission queue, held outside the channel's sequence for
the reason in 2.4. Per-user read cursors. An archival frequency per room, an
`archived_messages` table, the pass that moves rows into it — which shares the
sweep with transient expiry, since both must run when nobody is connected — and
an admin-only, pageable read over the archive that is a separate operation from
`history()` rather than a widening of it.

**Changed in the store, not merely extended.** `Timeline` must keep a per-room
high-water mark rather than deriving the next sequence from `MAX(seq)` over
surviving rows, and report it as `lastSeq`. Archival is the first thing in the
system that removes messages, and the current derivation is only correct while
nothing ever does. This is a small change with a wide blast radius: `lastSeq` is
what the client measures shortfall against, so an inaccurate one turns a visible
"earlier messages not shown" into a silently dropped message.

**Untouched.** The delivery machinery is orthogonal to all of it: per-room
sequence numbers, the gap repair in `client/src/core/chat.ts`, the ZeroMQ bus,
and the worker-liveness design. Those are the parts that are already right, and
two are reused rather than disturbed — the sweep that expires transient rooms,
and the sequencing the submission queue is built to avoid perturbing.
