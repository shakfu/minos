# Chat concepts

The model of conversation for minos, defined before and independently of any way
of reaching it. Nothing here is about windows, panes, tabs or terminals. If a
concept in this document can only be explained by describing a gesture, it is
not yet a concept.

The document is in two halves, because the model has two halves.

**Sections 2 and 3 are the core**: users, groups, rooms, admission, messages.
Everything here is load-bearing — remove any of it and something else stops
being definable. This is what a first implementation builds, and what any front
end needs in order to exist at all.

**Sections 4 and 5 are policy layered on the core**: retention and archival, and
the submission workflow that makes a channel curated rather than merely
broadcast. Both are valuable and neither is necessary. A system without them is
coherent: rooms simply keep everything, and a channel is a broadcast — which is
exactly what the existing `system` stream already is. They are specified here so
the core does not foreclose them, and deferred so the core can be built.

Section 7 is what the existing code has to change, split the same way.

## 1. Method

Three questions decide what any of these objects is. They are worth asking
explicitly because the answers are not obvious, and because the model this
replaces answered them by accident.

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
  different facts. A persisted room has participants who are usually absent; a
  transient room exists only while occupied. Without this split the lifetime of
  a transient room cannot be stated at all, because membership does not end —
  people do not resign from a conversation, they stop being in it.
- **Invitation is not subscription.** Rooms are closed: you are in one because
  someone with the authority to invite you did. Channels are open: you subscribe
  because you are interested. Different acts, by different people, revoked for
  different reasons.

# The core

## 2. The model

### 2.1 Users and roles

A **user** is an account that can author, read, and be assigned to groups.
Referenced by a stable id rather than a display name, so authorship survives a
rename and no username can impersonate the machine. The roster of who exists is
the host's business; `messaging/` already treats it as an injected `roster()`
and should continue to.

| Role | Scope | Confers |
|-|-|-|
| **Chat admin** | system | Creates permanent rooms and channels, and is the only one who may invite to a permanent room. Assigns group membership. |
| **Participant** | one room | Has access, by invitation. Reads and writes. In a user-created room, may also invite. |
| **Occupant** | one room, right now | Is present. Distinct from participant. |
| **Subscriber** | one channel | Receives what is published. By own choice. |
| **Creator** | one room | Raised it. Recorded as a fact; confers no authority yet — see core question 1. |

Roles are not a hierarchy. They are separate authorities over different objects,
and a user may hold several.

The important line is between **chat admin** and **ordinary user**, and it runs
through creation and invitation together:

> Permanent rooms are created and populated by admins. Ad-hoc rooms are created
> and populated by whoever wants one.

Permanent rooms are institutional: they represent an ongoing concern that
outlives any individual's interest, so who is in one is an administrative fact
and a participant cannot change it. Ad-hoc rooms are personal: any user raises
one by addressing people, and any participant may bring in another. That
permissiveness is not a concession — restricting invitation to the creator would
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
own conversation it would be a room whose access rule is itself, and the two
concepts would collapse. A group that wants to talk gets a room.

### 2.3 Room

**A place where a chat or a meeting happens.** Identity is the room itself, not
the people in it. Adding or removing a participant leaves the same room; two
rooms may have identical participants; a room everyone leaves is empty rather
than gone — except where its retention says otherwise.

#### Two properties, not three types

A room is described by two independent facts, and the familiar names are just
the combinations in use:

| | **Persisted** | **Transient** |
|-|-|-|
| **Admin-created** | *Permanent room* | unused — see core question 2 |
| **User-created** | *Ad-hoc persisted room* | *Ad-hoc transient room* |

- **Authority** — who created it, and therefore who may invite to it. An
  admin-created room is populated only by admins. A user-created room may be
  populated by any of its participants.
- **Retention** — whether it is kept. A persisted room lasts until deliberately
  deleted. A transient room is deleted a grace period after its last occupant
  leaves, and retains nothing.

Retention is chosen when the room is raised and never again; the reasons are
under *Transience* below. Nothing else about a room varies, which is why this is
two flags rather than a taxonomy.

##### Names and descriptions

Authority decides something else that is easy to miss: whether a room's title is
a **name** or a **description**.

An admin-created room is named. The title is institutional, chosen deliberately,
and exists to be referred to -- "post it in Engineering" only means something if
that resolves to one room. **So the name is unique among admin-created rooms, and
compared without case.** A second room by that name is refused, and in practice
the duplicate is nearly always an accident rather than an intent.

A user-created room is described. Its title renders who is in it, and **it need
not be unique.** Two ad-hoc rooms holding the same people are two different
conversations -- a different afternoon, a different subject -- and requiring
their titles to differ would be membership-as-identity returning by the back
door. They are told apart by when they began, which is how anybody would
distinguish them out loud.

The rule follows the authority axis rather than adding an axis of its own,
because uniqueness is only worth enforcing where a name is actually used to
refer: an institutional room is referred to across the organisation, a personal
one is not.

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

**A newly invited participant sees the room's history.** The history belongs to
the place, not to the people who happened to be present when it was written, so
admission is admission to all of it. There is no per-user bound and no
invited-since fact to store: every participant of a room sees exactly the same
thing. What varies is not who may see the history but how much of it the room
still holds, which is retention's business (section 4) rather than admission's.

#### Transience

A transient room exists for the duration of a conversation and is then deleted,
history included. This is the meeting-room analogue in the strict sense — the
room is released when the meeting is over and nothing is kept.

**It retains nothing.** Not on deletion, and not by any later change of mind:
there is no conversion that rescues what was said. The guarantee is
unconditional, which is the point. A promise of discard that someone with the
right button can retroactively withdraw is not a promise. Whether persistence is
wanted is therefore decided by the person raising the room, at that moment.

**It ends when the last occupant leaves, after a grace period.** The grace period
is not a detail — without it, a dropped connection, a page reload or a closed
laptop lid destroys a live conversation. The sequence is: occupancy falls to
zero, the room records the moment it emptied, and it is deleted once the grace
expires. Anyone re-entering before then cancels the deletion.

The duration is a genuine tension rather than a tuning exercise. Too short and an
accidental disconnect loses the conversation. Too long and the room lingers after
the participants believed it was gone — and deletion here is a promise made to
the people who spoke in it, not merely a retention policy. Somewhere around two
minutes is the right order of magnitude; it should be configurable, and it should
be documented to users, because they are relying on it.

Two mechanical consequences:

- **Emptiness must be stored, not timed in memory.** The room carries the moment
  its occupancy fell to zero, so any worker can decide whether the grace has
  expired. An in-process timer would be lost on restart and invisible to a second
  worker, and this server already runs several.
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
storage strategy — and the rule must be enforced by something that runs even when
nobody is watching, which is the sweep above.

### 2.4 Channel

**A read-only information stream.** A channel is not a conversation. Its
distinguishing property is that the right to publish and the right to read are
held by different people.

| | |
|-|-|
| Identity | An opaque id with a name. |
| Lifetime | Lasting, like a group. Independent of subscribers. |
| Authority | Subscribers read. Publication is not theirs. |

Channels are the one open object in the model. Where a room is closed and entered
by invitation, a channel is subscribed to by choice — the asymmetry is
deliberate, and it is the difference between a conversation and a broadcast.

In the core, a channel has one producer and no path for a subscriber to
contribute. The existing `system` stream is precisely this: a machine producer,
every user subscribed, nothing submitted. Letting users submit for approval is
section 5.

### 2.5 Messages, sequence and read state

A message belongs to exactly one room or channel and carries a **sequence
number** that is monotonic and contiguous within it. That number is the delivery
contract: the bus is fire-and-forget, so a client that sees a gap between its
cursor and what arrived asks for the difference. Nothing else detects a dropped,
duplicated or out-of-order frame.

Two cursors, and conflating them is a bug rather than a simplification:

- The **delivery cursor** answers *what have I received*, and exists to repair
  gaps. It lives in the client; `client/src/core/chat.ts` already implements it.
- The **read cursor** answers *what has this person seen*. It belongs on the
  server, because it is the same fact from every device and every interface.
  Conflating the two marks a message read by arriving.

A transient room needs no read state, since nothing survives to be unread.

**Sequence numbers are never reissued and never restart.** This costs nothing in
the core, where the only deletion is of a whole transient room, and it is what
makes retention possible later without breaking every client. Two cheap habits
now keep that door open:

- **The room stores its own high-water mark** rather than deriving the next
  sequence from `MAX(seq)` over surviving rows, as `Timeline._last_seq` does
  today. Correct now, and required the moment anything removes messages.
- **Messages are keyed by `(room_id, seq)`**, as they already are, so moving or
  re-moving a row is idempotent.

### 2.6 The core in one table

| | **Group** | **Room, admin-created** | **Room, user-created** | **Channel** |
|-|-|-|-|-|
| Created by | chat admin | chat admin | any user | chat admin |
| Who may admit | chat admin | chat admin only | any participant | nobody — self-subscribe |
| Admission is | assignment | invitation | invitation | subscription, by choice |
| Invitation names | — | user or group | user or group | — |
| Identity | opaque id | opaque id | opaque id | opaque id |
| Has a message log | **no** | yes | yes | yes |
| Who may write | — | participants | participants / occupants | the producer |
| Who may read | — | participants | participants / occupants | subscribers |
| Retention | n/a | persisted | persisted or transient | persisted |
| New participant sees | — | all history held | all history held | all history held |
| Survives being empty | yes | yes | persisted: yes; transient: **no** | yes |

## 3. Open questions in the core

1. **Who may delete a room, and can one be handed over?** Nothing yet says
   whether an admin may delete a user's persisted room, whether a creator may
   delete one others are actively using, or whether the role transfers when its
   creator leaves the organisation. Until this is answered, **creator confers no
   authority** and is only a recorded fact.

2. **May an admin raise a transient room?** The two axes admit the combination
   and nothing uses it. An admin calling a meeting that is deliberately not kept
   seems reasonable; if it is meant to be impossible, the reason should be
   stated, because otherwise it reads as an oversight.

3. **Should uniqueness extend beyond admin-created rooms?** Settled for now as
   admin-only: a permanent room's name is unique, an ad-hoc room's title is not,
   including when a user types one explicitly. The case for widening it is that
   a chosen name is a chosen name whoever typed it. The case against is that two
   people might each reasonably keep a "Planning" room, and colliding those
   across the whole server would surprise them. Worth revisiting once there are
   enough user-named rooms to see which way it bites.

4. **May a participant leave a room?** Under Place semantics leaving is not a
   change to the room but to that user's relationship with it. The harder half is
   what it means when access came from a group assignment rather than a personal
   invitation: leaving would be undone the moment the grant is re-evaluated, so
   either it cannot be left, or the departure is itself a stored fact that
   overrides the group grant.

5. **Is presence global or per-room?** Today it is global — online or not,
   published to the whole roster. Occupancy needs something finer, at least for
   transient rooms, where it decides when the room dies.

6. **Terminology: "system room".** Worth settling early, because `system` is
   already the id of the machine channel (`server/config.py`, `SYSTEM_STREAM`).
   Using "system room" as a synonym for an admin-created permanent room would
   collide with it in every log line and error message. Recommend reserving
   "system" for the machine channel and calling the other kind "permanent".

# Later

Neither section below is needed for a working system. Both are specified because
the core must not foreclose them, and the two habits in 2.5 are what ensure it
does not.

## 4. Retention and archival

**An admin may set an archival frequency on a room they create.** At each
interval the room's accumulated history is archived and leaves the live room. A
room with no frequency set keeps everything, which is what every room does in the
core.

The essential property: **archival bounds what exists, not what a newcomer may
see.** A participant admitted yesterday and one admitted five years ago see the
same messages — the ones since the last archival. Retention stays uniform across
participants, so this adds no per-user history rules to section 2.3.

**Archived history is stored, not destroyed, and the chat admin can read it.** It
leaves the live room and is beyond every participant's reach, including the
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
matters, so the interface has to be careful here whatever it looks like. If users
are told anything at all about archival, the honest sentence includes the admin.

### Sequence numbers must survive it

Archival is the first thing in the system that removes messages while the room
lives on, and the sequence contract in 2.5 is what it must not break.

If archival reset the count, a client holding cursor 500 would receive a new
message numbered 1, judge it already seen, and silently discard it — the message
would never appear, with nothing anywhere reporting a fault. So archival removes
messages from the low end and never renumbers what remains, and the room reports
its stored high-water mark as `lastSeq`. Deriving that from surviving rows, as
`Timeline._last_seq` does, is correct only while some remain: a room archived
down to empty would report zero and begin again at one, colliding with numbers
already issued.

The gap archival leaves is one the client already describes. A participant
returning with a cursor below what the room still holds is precisely the case
`client/src/core/chat.ts` was built for: the backfill starts above the cursor,
the shortfall is detected against `lastSeq`, and the log says `N earlier
message(s) not shown` rather than pretending. Archival is a second producer of a
condition the client can already report, so it needs no new protocol — provided
the high-water mark is honest.

### Where it lives

**The same database, in its own table.** Archived rows move from `messages` to an
`archived_messages` table beside it, keyed by `(room_id, seq)` as the live table
is. A row is archived unchanged — author, kind, body, timestamp and above all its
sequence number — so the archive is the room's history rather than a rendering of
it.

The point of the move is to keep the *hot table* small, and a separate table
achieves that completely: the live queries are all indexed on `(room_id, seq)`,
and both the backfill and the sequence lookup ride that index, so what matters is
how many rows the live table holds rather than how large the file is.

Keeping it in one file is what makes the move atomic. A transaction spanning two
attached SQLite databases is *not* atomic when the main database is in WAL mode,
and `Timeline.init` sets `PRAGMA journal_mode=WAL`. The failure is quiet: the
cross-database commit succeeds, returns no error and raises no warning — it
simply stops guaranteeing that both halves land. A crash mid-commit could then
delete rows from the live room without landing them in the archive, losing
precisely the messages the feature exists to keep. In one file the whole move is
a single `BEGIN IMMEDIATE`, which `Timeline._write` already provides.

Splitting the archive into its own file stays available, and the two habits in
2.5 are what keep it cheap. It should be done on evidence rather than in advance:
the archive running an order of magnitude ahead of the live table, backup or
checkpoint windows growing painful, or a wish to put the archive on different
storage under its own retention. The cost at that point is giving up the single
transaction for a two-phase move — archive, commit, then delete — which the
idempotent key makes safe.

**Not in the VFS, either way.** A file is the obvious home for an export, but the
VFS is user-visible by construction and this material is defined by not being
reachable by users. It belongs beside the timeline, where no mountpoint resolves.

### How an admin reads it

Not through `history()`. The live read is deliberately capped at the tail and
deliberately not pageable — "asking again from the same cursor returns the same
slice" — because its job is to repair a client's gap, and a client further behind
than the cap is told so rather than walked backwards through the room. An admin
reading an archive wants the opposite: a bounded, ordered, pageable walk over
material that is large by definition and not latency sensitive. It is a separate
operation with a separate authority check, and reusing the live one would
compromise both.

### Open questions

1. **Does the archive itself ever expire?** Nothing removes it, so the store
   holds every message ever sent, forever. That may be intended. If not, the
   limit belongs in the model rather than arriving later as an operational
   surprise — and it is the one place where "deleted" would finally be accurate.

2. **Is an admin's read of an archive recorded?** It is reading conversations
   whose participants can no longer reach them, which is the most sensitive act
   the model permits. The `system` channel already carries machine events and is
   the natural place to announce it. Announcing is a choice; doing it silently is
   also a choice.

3. **What are the frequency units, and what is the default?** Monthly or
   quarterly are the plausible shapes. More consequential is whether "never" is
   the default for an admin who sets nothing — the assumption above, and the safe
   one, since the alternative silently moves history out of reach that nobody
   chose to lose.

4. **Do user-created persisted rooms get retention?** Only admin-created rooms
   carry a frequency, so a user's persisted room keeps everything live forever
   and no one is accountable for it. That may be right — it is the user's own
   room — but it is the asymmetry most likely to be regretted, because it is
   where the volume accumulates.

## 5. Submissions and moderation

This is what turns a channel from a broadcast into a curated one.

A user may send a message to a channel. **It is not broadcast.** It becomes a
submission, visible to the channel's moderators, and it reaches subscribers only
if a moderator approves it. A **moderator** is a user who may publish directly
and may approve or reject what others submit.

The mechanism this requires is not the message log, and the reason is precise.
Per-channel sequence numbers are contiguous, and a client treats a gap as
evidence of loss and asks for a backfill that closes it. If a submission consumed
a sequence number and was then rejected, every subscriber would see a permanent
hole and re-request it forever. **A submission therefore lives outside the
channel's sequence** and is assigned one only on approval, at which point it
becomes an ordinary published message.

So a submission is a distinct object with its own lifecycle — submitted, then
approved or rejected — and the channel's log contains only what was published.

### Open questions

1. **What does a rejected submission tell its author?** Silence, a bare
   rejection, or a reason.

2. **May a moderator edit a submission before publishing?** "Curated" suggests
   yes. If so, is the published message attributed to its author or to the
   moderator who approved it?

3. **Who appoints moderators, and may a channel have none?** Presumably the chat
   admin, by symmetry with permanent rooms and channels. A channel with no
   moderator accepts submissions nobody can ever approve.

4. **Is channel subscription open to everyone?** Section 2.4 makes channels the
   open object, but a channel restricted to a group is an obvious want, and it
   would be a different mechanism from a room's invitation.

## 6. Deliberately excluded

Named so their absence is a decision rather than an oversight: threading and
replies; editing and deletion of published messages; reactions; typing
indicators; attachments as first-class objects rather than VFS paths mentioned in
text; federation across servers; calls or huddles layered on a room; and any
conversion of a transient room into a persisted one.

## 7. What this changes in the code

For orientation, not as a work plan. The existing `messaging/` implements a
retired model in which a room *was* a set of people.

### For the core

**Retired.** `merge` (`messaging/service.py:210`) folded one room's membership
into another's. It was a membership-as-identity gesture and has no meaning when a
room is a place — two places do not become one. The client-side pair dedupe at
`client/src/apps/Chat.ts:175` goes with it, along with the derived room title
frozen at creation and never recomputed.

**Reworked.** `open_room` gains a creation-authority check and a retention flag.
`invite` becomes the single admission mechanism, accepts a group as well as a
user, and is authority-checked per room type; grants that track a group must be
re-evaluated when assignments change, which is new behaviour rather than a
tightened check. Room `kind` stops being advisory: the service currently accepts
a `send` to a stream from any member (`service.py:166` checks membership, not
kind), which must be refused. `leave` currently revokes access to history the
leaver wrote, a side effect of `require_member` gating `history`
(`service.py:124`, `:153`); that needs revisiting alongside core question 3.

**New.** Groups, as a stored principal with assignments. The chat admin role.
Room retention, the stored empty-since moment, and the sweep that deletes
transient rooms once grace expires. Occupancy, distinct from access. Channel
subscriptions. Per-user read cursors. A stored per-room high-water mark, in place
of deriving the next sequence from `MAX(seq)`.

### For the later sections

An archival frequency per room, an `archived_messages` table, the pass that moves
rows into it — which shares the sweep with transient expiry, since both must run
when nobody is connected — and an admin-only, pageable read over the archive that
is a separate operation from `history()` rather than a widening of it. Then
moderators, and the submission queue held outside the channel's sequence.

### Untouched

The delivery machinery is orthogonal to all of it: per-room sequence numbers, the
gap repair in `client/src/core/chat.ts`, the ZeroMQ bus, and the worker-liveness
design. Those are the parts that are already right, and two are reused rather
than disturbed — the sweep that expires transient rooms, and the sequencing the
submission queue is built to avoid perturbing.
