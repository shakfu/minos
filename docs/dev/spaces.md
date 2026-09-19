# Spaces

An exploration of an object that groups rooms. Nothing here is built. The model it tests is [chat-concepts.md](chat-concepts.md); the wire it would change is [wire-contract.md](../wire-contract.md).

The case is [minos_agent_reqs.md](minos_agent_reqs.md): `pma` founds a room per task across 95 repositories, and the room list is flat, unordered and unbounded. Rooms need an organising principle and a lifecycle, and the model has neither.

**Decided: a space never decides admission.** chat-concepts 2.2 says of groups that a group with a message log "would be a room whose access rule is itself, and the two concepts would collapse". The symmetric discipline holds here. A space that granted access would be a group whose membership is implied by the rooms it contains. Section 3 is how access still works.

## 1. The gap

Table 2.6 lists four objects. One of them, the group, has no message log: it exists to be named where access is decided, and nothing else. It bundles principals.

Nothing bundles places. Two rooms about the same project are related by their titles and by nothing the server knows. The proposal is the symmetric object.

| | bundles | decides |
|-|-|-|
| group | users | who may be admitted |
| space | rooms and channels | where a room lives and what becomes of it |

## 2. What a space decides

| Property | Question | Today |
|-|-|-|
| Admission | Who may enter a room in it? | n/a; the room's grants decide |
| Retention | How long do its rooms keep messages? | per room, set by an admin |
| Lifetime | What deletes its rooms? | nothing, for a persisted room |
| Membership | How many spaces does a room belong to? | n/a |
| Order | In what order are its rooms listed? | the client's choice |
| Identity | What survives a rename? | the room's opaque id |

### 2.1 Admission: nothing

A space admits nobody. It has no participants, no subscribers and no occupancy. `sync` lists a space to a caller only when the caller may reach something in it, and listing it discloses its name and nothing more.

This is the invariant. chat-concepts 2.3 already refuses a second admission rule: "admission would then be decided by two rules that can disagree, and every question about who is in a room would have to ask both." A space that inherited access down to its rooms is that second rule.

### 2.2 Retention is inherited, and overridden per room

`archive.set` takes a `room` today (wire-contract 11). A space carries the same pair, `period` and `searchable`, and a room with no period of its own uses its space's.

This changes what a null period means. Today "a room or channel with no period keeps everything" (chat-concepts 4). Under a space it means "inherit, and keep everything if the space has none too". One rule, read in one order: room, then space, then keep everything.

This is the property a naming convention cannot give. "Every task room in `cynn` archives after 90 days" is one setting rather than one per room, and it applies to rooms founded after it was set.

### 2.3 Retention is not deletion

Archival removes messages from a live room and keeps them admin-readable (chat-concepts 4). The room remains. A space with an archival period therefore bounds what its rooms hold, and does not bound how many there are.

Deleting the rooms needs one of two things, and both are larger than this proposal:

- An answer to chat-concepts open question 1, which asks who may delete a persisted room and leaves it unanswered.
- A room lifetime on the space: a room with no message for N is deleted. New mechanism, and it inherits the transient room's whole problem -- a promise of deletion that must be enforced by a sweep running when nobody is watching (chat-concepts 2.3).

What a space does give is the unit such a policy is stated on. Section 7 is why that matters to `pma`.

### 2.4 A room belongs to zero or one space

A tree forces one parent; a tag allows many. Take one.

The reason is `pma` rather than tidiness. `pma dispatch cynn:31` names a task in one project and gives the run a worktree of that project. A task in one repository that unblocks another still belongs to the repository whose worktree it holds. One space is honest to how the work is actually divided.

Zero is allowed. A room outside every space is what every room is today.

### 2.5 Depth is one

`projects > cynn > task/31` is two levels of container and the proposal builds one: `cynn`, holding rooms.

The top level is a front end's tab bar, not a model level. `system` is a channel and `projects` would be a container; they appear side by side in a view and are not the same kind of object. Depth starts at spaces and stops there.

Nesting is deferred in section 6, with the test it would have to pass.

## 3. How access still works

Group per project, invited to each room in the space.

`pma` founds `task/31` in space `cynn` and invites group `proj-cynn`. A grant tracks the group (chat-concepts 2.3), so a colleague assigned to `proj-cynn` gains every room that named it, including rooms founded later, and unassigning revokes all of them at once.

This is the existing mechanism and it composes with spaces without touching them. The space says where the room lives; the group says who may enter. They meet when a room is founded and nowhere else.

The cost is one `invite` per room at creation, which `pma` does anyway.

## 4. Wire changes, sketched

### 4.1 Fields added to core shapes

- `space` on a room and on a channel: an id, or `null`. Additive, in the style of wire-contract 9.

- `spaces` on the sync object: a list of `{id, name, archive}`, where `archive` is the same `{period, searchable}` a room carries. Only spaces holding something the caller may reach.

### 4.2 Rules

- A space admits nobody. There is no `space.invite` and no member list.

- A room's `archive.period` of `null` inherits its space's. An explicit period overrides. A room in no space with no period keeps everything, as today.

- Changing a space's period applies at once to every room inheriting it, which is the rule a room already follows (chat-concepts 4).

- Moving a room between spaces changes what it inherits and nothing else. No grant is touched, and no message moves.

- Only an administrator creates, edits, or deletes a space, by symmetry with groups, permanent rooms and channels: every institutional fact in the model is the admin's.

### 4.3 Operations

| Op | Request fields | Reply |
|-|-|-|
| `space.create` | `name` | a space |
| `space.set` | `space`, `name?`, `period?`, `searchable?` | a space |
| `space.assign` | `room`, `space` or `null` | `{ok: true, room}` |
| `space.delete` | `space`, `rooms` | `{ok: true}` |

`create` and `open` gain an optional `space`, so a room is founded in one rather than founded and then moved.

`space.delete` must say what becomes of the rooms, and `rooms` takes `detach` or `delete`. `detach` is the safe one and should be the only one built until chat-concepts open question 1 is answered, because `delete` would otherwise invent a room-deletion authority the model has not decided.

These are socket operations, not routes. Sections 9 to 11 set the precedent that operations extend and routes do not (wire-contract 3).

## 5. Where navigation lives

A front end's. The server stores which space a room is in; it does not store which space anyone is looking at.

This follows the rule chat-concepts 2.5 uses for read state: a fact goes on the server when "it is the same fact from every device". Which space is open is not, any more than a chosen view is (channels.md 5).

The cheapest test of the navigation needs no wire change at all. Name rooms `cynn/31` and group by prefix in the terminal client. That answers within a day whether tabbing through projects is right, and it answers nothing about retention, bulk lifecycle, or an identity that survives a rename. Those three are what a real space has to earn.

## 6. Nesting, deferred

The test a second level must pass: name a question depth 3 answers that two flat attributes do not.

The plausible candidate is kind within project -- `cynn > tasks` beside `cynn > discussions`. That is a second axis, not a deeper tree, and a tree forces one of the two to be the parent. Two attributes on a room sort and filter without a client tracking a path.

Build one level. Add a `kind` if the case appears, and reach for depth only when something needs a path rather than a pair.

## 7. What this settles elsewhere

minos_agent_reqs.md leaves open what deletes a task room. Spaces do not answer it, and they change its shape usefully:

- A task room may stay admin-founded, which keeps invitation an administrator's act (wire-contract 7). That is what stops a worker inviting a second worker into its own room, and it is the property the ad-hoc alternative loses.

- The accumulation is then bounded in content by one setting per project, not one per room.

- The remaining question -- what removes the room object -- is stated on a space, where it is one policy per project rather than a decision per room.

The fork stands: admin-founded rooms keep invitation control and have no deletion path; ad-hoc rooms delete on their last grant and let any participant invite. A space does not resolve it. It makes the first option survivable while chat-concepts open question 1 is answered.

## 8. Open questions

1. Does `archive.search` span a space? "Search every task room in `cynn`" is the obvious want, and it is the pressure that would break section 2.1: a space-wide search must resolve to the union of rooms the caller may reach, and a careless version resolves to the space's audience, which a space does not have. Answer this before building search, not after.

2. Does a channel belong to a space? `system` suggests not, and a project's own feed suggests yes. Nothing here depends on it.

3. Is a space's name unique? Groups and rooms are identified by an opaque id with a name that need not be (chat-concepts 2.2). A space named in a client's navigation is read by people, so two spaces called `cynn` is a usability fault rather than a model one.

4. May a space hold rooms whose participants do not overlap at all? Nothing forbids it, and a space is then a filing decision rather than a statement about people. That is the intended reading, and it is worth stating so nobody later infers a shared audience from shared filing.

5. Who owns the space when a project is archived on GitHub but its rooms still hold history a dispute might need?
