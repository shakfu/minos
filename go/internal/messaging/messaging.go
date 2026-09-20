// Package messaging holds the conversation operations: groups, rooms, channels,
// moderation, occupancy and presence.
//
// A room is a place, not a set of people. Its identity is its own; adding or
// removing someone leaves the same room. Who may enter is a set of grants naming
// principals -- a user, or a whole group -- so admission is administrable rather
// than personal, and a grant to a group follows that group as it changes.
//
// Nothing here knows how its callers are connected. Operations return values and
// refusals are errors; deliveries go out through a Deliver callback the host
// provides. Whether a caller is an administrator is likewise the host's
// business, passed in rather than looked up.
//
// Delivery is a direct call rather than a publish. One process serves every
// connection, so `deliver(audience, event)` reaches the sockets in the same
// goroutine that appended the message. The sequence number still matters: it
// covers a reconnect, an hour offline and a lagging reader.
package messaging

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"minos/internal/timeline"
)

// Push types, as seen by a subscriber.
const (
	MessagePush     = "message"
	RoomPush        = "room"
	RoomGonePush    = "roomGone"
	PresencePush    = "presence"
	GroupPush       = "group"
	ProjectPush     = "project"
	ProjectGonePush = "projectGone"

	SubmissionPush = "submission"
	OpenedPush     = "opened"
	ArchivedPush   = "archived"
	ExitedPush     = "exited"
)

// ArchiveSearchLimit is the most messages one archive search returns.
const ArchiveSearchLimit = 100

// BodyLimit is the most bytes a message body or a rejection comment may hold,
// and NameLimit the most characters a title or a group name may have.
const (
	BodyLimit = 64 << 10
	NameLimit = 200
)

// TagLimit is the most characters a project tag may have, and TaskLimit the
// most a room's task label may have. Both are labels shown in a column, not
// prose.
const (
	TagLimit  = 32
	TaskLimit = 64
)

func checkBody(body string) error {
	if len(body) > BodyLimit {
		return refuse("A message is at most %d bytes", BodyLimit)
	}
	return nil
}

func checkName(name string) error {
	if len([]rune(name)) > NameLimit {
		return refuse("A name is at most %d characters", NameLimit)
	}
	return nil
}

// Refusal is a request that cannot be carried out, reportable to whoever asked.
// Distinct from a bug: the host turns this into an error reply and anything else
// into a generic failure.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }

func refuse(format string, args ...any) error {
	return &Refusal{Message: fmt.Sprintf(format, args...)}
}

// Deliver hands one event to everyone in `audience`.
type Deliver func(audience []string, event any)

// Messaging is one server's view of the conversation.
type Messaging struct {
	store   *timeline.Timeline
	deliver Deliver
	roster  func() []string
}

func New(store *timeline.Timeline, deliver Deliver, roster func() []string) *Messaging {
	return &Messaging{store: store, deliver: deliver, roster: roster}
}

func (m *Messaging) Store() *timeline.Timeline { return m.store }

// -- push shapes -------------------------------------------------------------

type messageEvent struct {
	Type string `json:"type"`
	timeline.Message
}

type roomEvent struct {
	Type string         `json:"type"`
	Room *timeline.Room `json:"room"`
}

type roomGoneEvent struct {
	Type string `json:"type"`
	Room string `json:"room"`
}

type presenceEvent struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Online   bool   `json:"online"`
}

type projectEvent struct {
	Type    string            `json:"type"`
	Project *timeline.Project `json:"project"`
}

type projectGoneEvent struct {
	Type    string `json:"type"`
	Project string `json:"project"`
}

type groupEvent struct {
	Type  string          `json:"type"`
	Group *timeline.Group `json:"group"`
}

type submissionEvent struct {
	Type       string               `json:"type"`
	Submission *timeline.Submission `json:"submission"`
}

type openedEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Seq     int64  `json:"seq"`
}

type archivedEvent struct {
	Type    string `json:"type"`
	Room    string `json:"room"`
	Through int64  `json:"through"`
}

type exitedEvent struct {
	Type      string `json:"type"`
	Room      string `json:"room"`
	Occupancy string `json:"occupancy"`
}

// -- replies -----------------------------------------------------------------

// UserState is one account and whether it is connected.
type UserState struct {
	Username string `json:"username"`
	Online   bool   `json:"online"`
}

// SyncReply is everything a freshly connected client needs.
type SyncReply struct {
	Me      string           `json:"me"`
	IsAdmin bool             `json:"isAdmin"`
	Users   []UserState      `json:"users"`
	Groups  []timeline.Group `json:"groups"`
	// Every project, whoever is asking: a project decides no access, so there
	// is nothing to filter. A room names the one it is filed under.
	Projects []timeline.Project `json:"projects"`
	Rooms    []timeline.Room    `json:"rooms"`
	Channels []timeline.Room    `json:"channels"`
	Read     map[string]int64   `json:"read"`
	// Rooms the caller has ever entered. An invitation is open until then.
	Visited []string `json:"visited"`
	// The caller's own submissions, pending or rejected and not acknowledged.
	// Here rather than only on a push, so an author who was offline for the
	// decision still learns it.
	Submissions []timeline.Submission `json:"submissions"`
}

// HistoryReply carries the backfill and where the room actually ends.
type HistoryReply struct {
	Room     string             `json:"room"`
	Since    int64              `json:"since"`
	LastSeq  int64              `json:"lastSeq"`
	Messages []timeline.Message `json:"messages"`
	// On a channel, which of these messages the caller has opened. Absent on a
	// room, which keeps a read cursor instead.
	Opened *[]int64 `json:"opened,omitempty"`
}

// Ok is the shape of an operation that answers nothing else.
type Ok struct {
	Ok bool `json:"ok"`
}

type SendReply struct {
	Ok  bool  `json:"ok"`
	Seq int64 `json:"seq"`
}

type ProjectReply struct {
	Ok      bool              `json:"ok"`
	Project *timeline.Project `json:"project"`
}

type RoomReply struct {
	Ok   bool           `json:"ok"`
	Room *timeline.Room `json:"room"`
}

type ChannelReply struct {
	Ok      bool           `json:"ok"`
	Channel *timeline.Room `json:"channel"`
}

type EnterReply struct {
	Ok        bool   `json:"ok"`
	Occupancy string `json:"occupancy"`
	Room      string `json:"room"`
}

type ReadReply struct {
	Ok   bool   `json:"ok"`
	Room string `json:"room"`
	Seq  int64  `json:"seq"`
}

// QueueReply is what a channel's moderators have to decide.
type QueueReply struct {
	Channel     string                `json:"channel"`
	Submissions []timeline.Submission `json:"submissions"`
}

type OpenReply struct {
	Ok      bool   `json:"ok"`
	Channel string `json:"channel"`
	Seq     int64  `json:"seq"`
}

// ArchiveReadReply is one page of an archive, and whether more follows.
type ArchiveReadReply struct {
	Room     string             `json:"room"`
	After    int64              `json:"after"`
	Messages []timeline.Message `json:"messages"`
	More     bool               `json:"more"`
}

type ArchiveSearchReply struct {
	Room     string             `json:"room"`
	Query    string             `json:"query"`
	Messages []timeline.Message `json:"messages"`
}

// ArchiveChange is an archive.set request, as it arrived. A field not given is
// left as it is; a nil Period that was given means never.
type ArchiveChange struct {
	Period          any
	PeriodGiven     bool
	Searchable      any
	SearchableGiven bool
}

// -- access ------------------------------------------------------------------

// requireRoom returns the room, if it exists and is of the wanted kind.
func (m *Messaging) requireRoom(roomID, kind string) (*timeline.Room, error) {
	room, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	if room == nil || (kind != "" && room.Kind != kind) {
		return nil, refuse("No such room: %s", roomID)
	}
	return room, nil
}

// requireAccess returns the room, if this user may see it. Groups count.
func (m *Messaging) requireAccess(roomID, username, kind string) (*timeline.Room, error) {
	room, err := m.requireRoom(roomID, kind)
	if err != nil {
		return nil, err
	}
	permitted, err := m.store.HasAccess(roomID, username)
	if err != nil {
		return nil, err
	}
	if !permitted {
		return nil, refuse("Not invited to that room")
	}
	return room, nil
}

// requireInviteAuthority decides who may bring someone into this room.
//
// Admin-founded rooms are institutional: their membership is an administrative
// fact, so a participant cannot change it. User-founded rooms are permissive,
// and deliberately so -- restricting invitation to the creator would buy
// nothing, since any participant could raise a new room with the same people.
func (m *Messaging) requireInviteAuthority(room *timeline.Room, username string, isAdmin bool) error {
	if room.Authority == timeline.Admin {
		if !isAdmin {
			return refuse("Only an administrator may invite to this room")
		}
		return nil
	}
	permitted, err := m.store.HasAccess(room.ID, username)
	if err != nil {
		return err
	}
	if !permitted {
		return refuse("Not invited to that room")
	}
	return nil
}

func requireAdmin(isAdmin bool) error {
	if !isAdmin {
		return refuse("Only an administrator may do that")
	}
	return nil
}

// -- sync and history --------------------------------------------------------

func (m *Messaging) Sync(username string, isAdmin bool) (*SyncReply, error) {
	rooms, err := m.store.RoomsFor(username)
	if err != nil {
		return nil, err
	}
	channels, err := m.store.ChannelsFor(username)
	if err != nil {
		return nil, err
	}
	groups, err := m.store.Groups()
	if err != nil {
		return nil, err
	}
	projects, err := m.store.Projects()
	if err != nil {
		return nil, err
	}
	cursors, err := m.store.ReadCursors(username)
	if err != nil {
		return nil, err
	}
	submissions, err := m.store.SubmissionsBy(username)
	if err != nil {
		return nil, err
	}
	visited, err := m.store.Visited(username)
	if err != nil {
		return nil, err
	}

	online := m.store.Online()
	names := m.roster()
	sort.Strings(names)

	users := make([]UserState, 0, len(names))
	for _, name := range names {
		users = append(users, UserState{Username: name, Online: online[name]})
	}

	return &SyncReply{
		Me: username, IsAdmin: isAdmin, Users: users,
		Groups: groups, Projects: projects, Rooms: rooms, Channels: channels, Read: cursors,
		Visited: visited, Submissions: submissions,
	}, nil
}

func (m *Messaging) History(username, roomID string, since int64) (*HistoryReply, error) {
	room, err := m.requireAccess(roomID, username, "")
	if err != nil {
		return nil, err
	}
	messages, err := m.store.History(roomID, since)
	if err != nil {
		return nil, err
	}
	// lastSeq is what makes a truncated reply detectable: the cap is on the
	// tail, so a client further behind than the limit gets the newest slice and
	// would otherwise have no way to know it skipped the rest.
	reply := &HistoryReply{Room: roomID, Since: since, LastSeq: room.LastSeq, Messages: messages}
	if room.Kind == timeline.ChannelKind {
		opened := []int64{}
		if len(messages) > 0 {
			opened, err = m.store.OpenedBetween(
				roomID, username, messages[0].Seq, messages[len(messages)-1].Seq)
			if err != nil {
				return nil, err
			}
		}
		reply.Opened = &opened
	}
	return reply, nil
}

func (m *Messaging) Send(username, roomID, body string) (*SendReply, error) {
	room, err := m.requireAccess(roomID, username, "")
	if err != nil {
		return nil, err
	}
	if room.Kind == timeline.ChannelKind {
		// A channel is read-only to its audience. What a subscriber may do is
		// propose a message, and that is Submit: a separate operation, because a
		// proposal is not a message until a moderator approves it.
		return nil, refuse("A channel is read-only")
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, refuse("Empty message")
	}
	if err := checkBody(body); err != nil {
		return nil, err
	}

	message, err := m.store.Append(roomID, username, "", body, timeline.Text)
	if err != nil {
		return nil, err
	}
	m.deliver(room.Audience, messageEvent{Type: MessagePush, Message: message})
	return &SendReply{Ok: true, Seq: message.Seq}, nil
}

// -- rooms -------------------------------------------------------------------

// OpenRoom raises an ad-hoc room. Any user may; the creator is a participant.
//
// Retention is chosen here and never again: a transient room cannot later be
// kept, because it retains nothing to keep.
func (m *Messaging) OpenRoom(username string, invitees []any, title, retention string) (*timeline.Room, error) {
	if retention != timeline.Persisted && retention != timeline.Transient {
		return nil, refuse("No such retention: %s", retention)
	}

	grants, err := m.grantsFor(username, invitees)
	if err != nil {
		return nil, err
	}

	title = strings.TrimSpace(title)
	if err := checkName(title); err != nil {
		return nil, err
	}
	if title == "" {
		title, err = m.deriveTitle(grants)
		if err != nil {
			return nil, err
		}
	}

	room, err := m.store.CreateRoom(
		title, username, timeline.RoomKind, timeline.User, retention, "", grants, timeline.Filing{})
	if err != nil {
		return nil, err
	}
	m.announceRoom(room, nil)
	return room, nil
}

// CreateRoom founds a permanent room. Admins only, and always persisted.
func (m *Messaging) CreateRoom(
	username string, isAdmin bool, title string, invitees []any, project, scope, task string,
) (*timeline.Room, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, refuse("A permanent room needs a name")
	}
	if err := checkName(title); err != nil {
		return nil, err
	}

	filed, err := m.filing(project, scope, task)
	if err != nil {
		return nil, err
	}
	if err := m.nameIsFree(title, timeline.RoomKind, filed.Project); err != nil {
		return nil, err
	}

	grants, err := m.grantsFor(username, invitees)
	if err != nil {
		return nil, err
	}

	room, err := m.store.CreateRoom(
		title, username, timeline.RoomKind, timeline.Admin, timeline.Persisted, "", grants, filed)
	if err != nil {
		return nil, err
	}
	m.announceRoom(room, nil)
	return room, nil
}

// grantsFor is the creator plus every invitee, deduplicated and sorted.
func (m *Messaging) grantsFor(username string, invitees []any) ([]timeline.Principal, error) {
	seen := map[timeline.Principal]bool{{Kind: timeline.PrincipalUser, ID: username}: true}
	for _, invitee := range invitees {
		principal, err := m.principal(invitee)
		if err != nil {
			return nil, err
		}
		seen[principal] = true
	}

	grants := make([]timeline.Principal, 0, len(seen))
	for principal := range seen {
		grants = append(grants, principal)
	}
	sortPrincipals(grants)
	return grants, nil
}

func sortPrincipals(grants []timeline.Principal) {
	sort.Slice(grants, func(a, b int) bool {
		if grants[a].Kind != grants[b].Kind {
			return grants[a].Kind < grants[b].Kind
		}
		return grants[a].ID < grants[b].ID
	})
}

// principal normalises one invitee into a pair that exists.
func (m *Messaging) principal(value any) (timeline.Principal, error) {
	var none timeline.Principal

	kind, identifier := timeline.PrincipalUser, ""
	switch typed := value.(type) {
	case string:
		identifier = typed
	case map[string]any:
		if named, ok := typed["kind"].(string); ok {
			kind = named
		}
		text, ok := typed["id"].(string)
		if !ok {
			return none, refuse("Not a principal: %s", repr(value))
		}
		identifier = text
	default:
		return none, refuse("Not a principal: %s", repr(value))
	}

	switch kind {
	case timeline.PrincipalUser:
		if !m.known(identifier) {
			return none, refuse("No such user: %s", identifier)
		}
	case timeline.PrincipalGroup:
		group, err := m.store.Group(identifier)
		if err != nil {
			return none, err
		}
		if group == nil {
			return none, refuse("No such group: %s", identifier)
		}
	default:
		return none, refuse("No such principal kind: %s", kind)
	}
	return timeline.Principal{Kind: kind, ID: identifier}, nil
}

func (m *Messaging) known(username string) bool {
	for _, name := range m.roster() {
		if name == username {
			return true
		}
	}
	return false
}

// deriveTitle names a room nobody named: who is in it.
func (m *Messaging) deriveTitle(grants []timeline.Principal) (string, error) {
	names := make([]string, 0, len(grants))
	for _, grant := range grants {
		if grant.Kind == timeline.PrincipalGroup {
			group, err := m.store.Group(grant.ID)
			if err != nil {
				return "", err
			}
			if group != nil {
				names = append(names, group.Name)
				continue
			}
		}
		names = append(names, grant.ID)
	}
	if len(names) == 0 {
		return "Room", nil
	}
	return strings.Join(names, ", "), nil
}

// Invite admits a principal. The room's authority decides who may.
func (m *Messaging) Invite(username string, isAdmin bool, roomID string, value any) (*RoomReply, error) {
	room, err := m.requireRoom(roomID, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	if err := m.requireInviteAuthority(room, username, isAdmin); err != nil {
		return nil, err
	}
	principal, err := m.principal(value)
	if err != nil {
		return nil, err
	}

	added, err := m.store.AddGrant(roomID, principal.Kind, principal.ID)
	if err != nil {
		return nil, err
	}
	if !added {
		return &RoomReply{Ok: true, Room: room}, nil
	}

	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	if _, err := m.PostEvent(roomID, username+" invited "+m.label(principal), updated.Audience); err != nil {
		return nil, err
	}

	updated, err = m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, nil)
	return &RoomReply{Ok: true, Room: updated}, nil
}

// Uninvite withdraws a grant. Same authority as issuing one.
func (m *Messaging) Uninvite(username string, isAdmin bool, roomID string, value any) (*RoomReply, error) {
	room, err := m.requireRoom(roomID, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	if err := m.requireInviteAuthority(room, username, isAdmin); err != nil {
		return nil, err
	}
	principal, err := m.principal(value)
	if err != nil {
		return nil, err
	}

	updated, err := m.withdraw(room, principal, username+" removed "+m.label(principal))
	if err != nil && !errors.Is(err, errNotGranted) {
		return nil, err
	}
	return &RoomReply{Ok: true, Room: updated}, nil
}

// Leave gives up one's own place in a room.
//
// Only a grant naming the user directly can be given up. Access inherited from a
// group is not this user's to drop -- it would be restored the moment the grant
// was re-evaluated -- so leaving such a room is refused rather than silently
// undone later.
func (m *Messaging) Leave(username, roomID string) (*Ok, error) {
	room, err := m.requireAccess(roomID, username, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	principal := timeline.Principal{Kind: timeline.PrincipalUser, ID: username}
	if _, err := m.withdraw(room, principal, username+" left"); errors.Is(err, errNotGranted) {
		return nil, refuse("Access to this room comes from a group, so it cannot be left")
	} else if err != nil {
		return nil, err
	}
	return &Ok{Ok: true}, nil
}

// errNotGranted is withdraw's report that the grant was not there to remove.
var errNotGranted = errors.New("not granted")

// withdraw removes a grant and tells everyone it concerns. It returns the room
// as it now is, or nil when the grant was an ad-hoc room's last and the room is
// gone. A grant that was not there changes nothing and returns errNotGranted,
// which Uninvite takes as success and Leave as a refusal.
func (m *Messaging) withdraw(room *timeline.Room, principal timeline.Principal, event string) (*timeline.Room, error) {
	// Read before the change: afterwards the people removed are not in the
	// audience, and this is the last moment their clients can be told.
	before := room.Audience

	withdrawal, err := m.store.RemoveGrant(room.ID, principal.Kind, principal.ID)
	if err != nil {
		return nil, err
	}
	switch withdrawal {
	case timeline.NotGranted:
		return room, errNotGranted
	case timeline.LastKept:
		return nil, refuse("A permanent room keeps its last grant")
	case timeline.RoomDeleted:
		if err := m.release(room.ID, before); err != nil {
			return nil, err
		}
		m.gone(room.ID, before)
		return nil, nil
	}

	if _, err := m.PostEvent(room.ID, event, before); err != nil {
		return nil, err
	}
	updated, err := m.store.Room(room.ID)
	if err != nil {
		return nil, err
	}
	dropped := without(before, updated.Audience)
	if err := m.release(room.ID, dropped); err != nil {
		return nil, err
	}
	if updated, err = m.store.Room(room.ID); err != nil {
		return nil, err
	}
	m.announceRoom(updated, before)
	m.gone(room.ID, dropped)
	return updated, nil
}

// release gives up each user's place in a room they can no longer reach.
func (m *Messaging) release(roomID string, usernames []string) error {
	for _, name := range usernames {
		if _, err := m.store.Release(roomID, name); err != nil {
			return err
		}
	}
	return nil
}

// gone tells each user's clients to drop a room.
func (m *Messaging) gone(roomID string, usernames []string) {
	for _, name := range usernames {
		m.deliver([]string{name}, roomGoneEvent{Type: RoomGonePush, Room: roomID})
	}
}

// label names a principal in an event line: a group by its name.
func (m *Messaging) label(principal timeline.Principal) string {
	if principal.Kind == timeline.PrincipalGroup {
		if group, err := m.store.Group(principal.ID); err == nil && group != nil {
			return "group " + group.Name
		}
	}
	return principal.ID
}

// without is every name in first that second lacks.
func without(first, second []string) []string {
	keeping := map[string]bool{}
	for _, name := range second {
		keeping[name] = true
	}
	var missing []string
	for _, name := range first {
		if !keeping[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// -- occupancy ---------------------------------------------------------------

// Enter takes a place in a room. Distinct from being invited to it.
//
// A transient room's life is measured from the moment its last occupant leaves,
// so this is what keeps one alive -- and what rescues one during its grace.
//
// A person is in one room at a time, so entering first leaves any other room
// they are in, on any connection. The connection that held it is told with an
// exited push; the same room on another connection is left alone.
func (m *Messaging) Enter(username, roomID string) (*EnterReply, error) {
	room, err := m.requireAccess(roomID, username, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	for _, held := range m.store.OccupanciesElsewhere(username, roomID) {
		left, err := m.store.Exit(held)
		if err != nil {
			return nil, err
		}
		if left == "" {
			continue
		}
		if previous, err := m.store.Room(left); err == nil && previous != nil {
			m.announceRoom(previous, nil)
		}
		m.deliver([]string{username}, exitedEvent{Type: ExitedPush, Room: left, Occupancy: held})
	}

	occupancy, err := m.store.Enter(roomID, username)
	if errors.Is(err, timeline.ErrNoRoom) {
		return nil, refuse("No such room: %s", roomID)
	}
	if err != nil {
		return nil, err
	}

	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, nil)
	return &EnterReply{Ok: true, Occupancy: occupancy, Room: room.ID}, nil
}

// Exit gives up a place. Starts the countdown if it was the last one.
func (m *Messaging) Exit(occupancyID string) (*Ok, error) {
	roomID, err := m.store.Exit(occupancyID)
	if err != nil {
		return nil, err
	}
	if roomID != "" {
		if room, err := m.store.Room(roomID); err == nil && room != nil {
			m.announceRoom(room, nil)
		}
	}
	return &Ok{Ok: true}, nil
}

// Sweep deletes transient rooms whose grace period has run out, then archives
// aged messages. One pass, because both must run when nobody is connected.
//
// The audience is read before a room goes, because afterwards there is nobody
// to tell: a client that is not told would keep the room in its list forever.
func (m *Messaging) Sweep() ([]string, error) {
	expired, err := m.store.ExpiredTransientRooms()
	if err != nil {
		return nil, err
	}

	gone := make([]string, 0, len(expired))
	for _, roomID := range expired {
		var audience []string
		if room, err := m.store.Room(roomID); err == nil && room != nil {
			audience = room.Audience
		}
		deleted, err := m.store.DeleteExpired(roomID)
		if err != nil {
			return gone, err
		}
		if !deleted {
			continue // entered since it was listed
		}
		m.deliver(audience, roomGoneEvent{Type: RoomGonePush, Room: roomID})
		gone = append(gone, roomID)
	}
	return gone, m.archiveAged()
}

// archiveAged moves each archiving room's aged messages out, and tells its
// audience which to drop.
func (m *Messaging) archiveAged() error {
	archiving, err := m.store.Archiving()
	if err != nil {
		return err
	}
	for _, roomID := range archiving {
		through, err := m.store.ArchiveAged(roomID)
		if err != nil {
			return err
		}
		if through == 0 {
			continue
		}
		audience, err := m.store.Audience(roomID)
		if err != nil {
			return err
		}
		m.deliver(audience, archivedEvent{Type: ArchivedPush, Room: roomID, Through: through})
	}
	return nil
}

// -- read state --------------------------------------------------------------

func (m *Messaging) MarkRead(username, roomID string, seq int64) (*ReadReply, error) {
	room, err := m.requireAccess(roomID, username, "")
	if err != nil {
		return nil, err
	}
	if room.Kind == timeline.ChannelKind {
		// Items opened in any order are not something one cursor can record.
		return nil, refuse("A channel's items are opened, not read")
	}
	if err := m.store.MarkRead(roomID, username, seq); err != nil {
		return nil, err
	}
	return &ReadReply{Ok: true, Room: roomID, Seq: seq}, nil
}

// -- groups ------------------------------------------------------------------

func (m *Messaging) CreateGroup(isAdmin bool, name string, members []any) (*timeline.Group, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, refuse("A group needs a name")
	}
	if err := checkName(name); err != nil {
		return nil, err
	}

	// An unknown member is dropped rather than refused: the group is a set of
	// this server's accounts, and naming somebody else is not a request it can
	// carry out but is not worth failing over either.
	known := []string{}
	for _, member := range members {
		if text, ok := member.(string); ok && m.known(text) {
			known = append(known, text)
		}
	}
	sort.Strings(known)

	group, err := m.store.CreateGroup(name, "", known)
	if err != nil {
		return nil, err
	}
	m.announceGroup(group)
	return group, nil
}

// AssignGroup adds a user to a group, and with it every room the group holds.
func (m *Messaging) AssignGroup(isAdmin bool, groupID, username string) (*timeline.Group, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	group, err := m.store.Group(groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, refuse("No such group: %s", groupID)
	}
	if !m.known(username) {
		return nil, refuse("No such user: %s", username)
	}

	if err := m.store.AssignGroup(groupID, username); err != nil {
		return nil, err
	}
	group, err = m.store.Group(groupID)
	if err != nil {
		return nil, err
	}
	m.announceGroup(group)

	// The new member's room list just changed, and nothing else would tell
	// them: they were not in the audience of any of those rooms a moment ago.
	// Channels for the same reason: a restricted one this group admits is theirs
	// again if they had subscribed to it before losing eligibility.
	spaces, err := m.spacesFor(username)
	if err != nil {
		return nil, err
	}
	for index := range spaces {
		m.announceRoom(&spaces[index], nil)
	}
	return group, nil
}

func (m *Messaging) UnassignGroup(isAdmin bool, groupID, username string) (*timeline.Group, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	group, err := m.store.Group(groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, refuse("No such group: %s", groupID)
	}

	// Read before the change: afterwards these rooms are no longer theirs, so
	// this is the last moment their client can be told to drop them. A restricted
	// channel is lost the same way, and leaves the subscription behind -- being
	// removed from a group is not unsubscribing.
	before, err := m.spacesFor(username)
	if err != nil {
		return nil, err
	}
	if err := m.store.UnassignGroup(groupID, username); err != nil {
		return nil, err
	}
	after, err := m.spacesFor(username)
	if err != nil {
		return nil, err
	}

	keeping := map[string]bool{}
	for _, room := range after {
		keeping[room.ID] = true
	}

	group, err = m.store.Group(groupID)
	if err != nil {
		return nil, err
	}
	m.announceGroup(group)

	for _, room := range before {
		if keeping[room.ID] {
			continue
		}
		released, err := m.store.Release(room.ID, username)
		if err != nil {
			return nil, err
		}
		if released > 0 {
			if updated, err := m.store.Room(room.ID); err == nil {
				m.announceRoom(updated, nil)
			}
		}
		m.deliver([]string{username}, roomGoneEvent{Type: RoomGonePush, Room: room.ID})
	}
	return group, nil
}

// spacesFor is every room and channel this user currently reaches.
func (m *Messaging) spacesFor(username string) ([]timeline.Room, error) {
	rooms, err := m.store.RoomsFor(username)
	if err != nil {
		return nil, err
	}
	channels, err := m.store.ChannelsFor(username)
	if err != nil {
		return nil, err
	}
	return append(rooms, channels...), nil
}

// -- projects ----------------------------------------------------------------

// CreateProject founds a project. Names are compared without case and must be
// unique, because a project is named where a room is filed and two that differ
// only in capitalisation would not help anybody tell them apart.
func (m *Messaging) CreateProject(isAdmin bool, name string, tags []any) (*ProjectReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, refuse("A project needs a name")
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	// A place is referred to from outside its project as `<project>/<title>`,
	// split at the first slash. A project whose name held one would make that
	// ambiguous; a room title may still hold one, because everything after the
	// first slash is the title.
	if strings.Contains(name, "/") {
		return nil, refuse("A project name has no '/' in it")
	}
	existing, err := m.store.ProjectNamed(name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, refuse("There is already a project called %s", existing.Name)
	}

	wanted, err := checkTags(tags)
	if err != nil {
		return nil, err
	}

	project, err := m.store.CreateProject(name, "", wanted)
	if err != nil {
		return nil, err
	}
	m.announceProject(project)
	return &ProjectReply{Ok: true, Project: project}, nil
}

// checkTags normalises a list of tags: trimmed, folded to lower case, and
// without repeats. Folded because a tag exists to be filtered on, and `Go` and
// `go` filtering apart would divide the projects rather than classify them.
func checkTags(tags []any) ([]string, error) {
	wanted := []string{}
	for _, value := range tags {
		text, ok := value.(string)
		if !ok {
			return nil, refuse("A tag is text")
		}
		tag, err := checkTag(text)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(wanted, tag) {
			wanted = append(wanted, tag)
		}
	}
	sort.Strings(wanted)
	return wanted, nil
}

func checkTag(text string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(text))
	if tag == "" {
		return "", refuse("A tag needs text")
	}
	if len([]rune(tag)) > TagLimit {
		return "", refuse("A tag is at most %d characters", TagLimit)
	}
	if strings.ContainsAny(tag, " \t\n") {
		return "", refuse("A tag is one word")
	}
	return tag, nil
}

// Tag and Untag classify a project. Tagging one that already carries the tag,
// and untagging one that does not, both answer ok: the caller asked for a
// state, not for a change.
func (m *Messaging) Tag(isAdmin bool, projectID, text string, remove bool) (*ProjectReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	project, err := m.requireProject(projectID)
	if err != nil {
		return nil, err
	}
	tag, err := checkTag(text)
	if err != nil {
		return nil, err
	}

	if remove {
		_, err = m.store.Untag(project.ID, tag)
	} else {
		_, err = m.store.Tag(project.ID, tag)
	}
	if err != nil {
		return nil, err
	}
	updated, err := m.store.Project(project.ID)
	if err != nil {
		return nil, err
	}
	m.announceProject(updated)
	return &ProjectReply{Ok: true, Project: updated}, nil
}

func (m *Messaging) requireProject(projectID string) (*timeline.Project, error) {
	project, err := m.store.Project(projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, refuse("No such project: %s", projectID)
	}
	return project, nil
}

// FileRoom files a room or a channel under a project, or under none when
// projectID is empty. A room belongs to at most one project, and its scope is
// its position within that project: filing under none clears both.
func (m *Messaging) FileRoom(
	isAdmin bool, roomID, projectID, scope, task string,
) (*RoomReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	room, err := m.requireRoom(roomID, "")
	if err != nil {
		return nil, err
	}
	// A transient room is discarded when everyone leaves, so filing it under a
	// project records a place that is about to stop existing.
	if room.Retention == timeline.Transient {
		return nil, refuse("A transient room is not filed under a project")
	}
	if projectID != "" {
		if _, err := m.requireProject(projectID); err != nil {
			return nil, err
		}
	}
	scope, task, err = checkScope(projectID, scope, task)
	if err != nil {
		return nil, err
	}
	// Moving it somewhere its name is taken would make two rooms answer to one
	// qualified name.
	if room.Authority == timeline.Admin && projectID != room.Project {
		if err := m.nameIsFree(room.Title, room.Kind, projectID); err != nil {
			return nil, err
		}
	}

	if _, err := m.store.FileRoom(roomID, projectID, scope, task); err != nil {
		return nil, err
	}
	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, nil)
	return &RoomReply{Ok: true, Room: updated}, nil
}

// checkScope decides what a room's scope and task may be. A scope is a
// position within a project, so a room under none has neither; a task names
// one, so only a task room carries it. The label itself is opaque: which task
// it names is pma's business and not the server's.
func checkScope(projectID, scope, task string) (string, string, error) {
	scope, task = strings.TrimSpace(scope), strings.TrimSpace(task)
	if projectID == "" {
		if scope != "" || task != "" {
			return "", "", refuse("A room under no project has no scope")
		}
		return "", "", nil
	}
	switch scope {
	case "":
		if task != "" {
			return "", "", refuse("Only a task room names a task")
		}
		// Filed without saying what it is about: the project as a whole.
		return timeline.ProjectScope, "", nil
	case timeline.ProjectScope:
		if task != "" {
			return "", "", refuse("Only a task room names a task")
		}
	case timeline.TaskScope:
		if task == "" {
			return "", "", refuse("A task room names its task")
		}
		if len([]rune(task)) > TaskLimit {
			return "", "", refuse("A task is at most %d characters", TaskLimit)
		}
	default:
		return "", "", refuse("A scope is %q or %q", timeline.ProjectScope, timeline.TaskScope)
	}
	return scope, task, nil
}

// filing validates where a new place is to sit: its project must exist, and
// its scope must suit it.
func (m *Messaging) filing(project, scope, task string) (timeline.Filing, error) {
	if project != "" {
		if _, err := m.requireProject(project); err != nil {
			return timeline.Filing{}, err
		}
	}
	scope, task, err := checkScope(project, scope, task)
	if err != nil {
		return timeline.Filing{}, err
	}
	return timeline.Filing{Project: project, Scope: scope, Task: task}, nil
}

// nameIsFree refuses a title already taken in the project a place is going
// into.
//
// A permanent room's title is a name: institutional, chosen, and meant to be
// referred to. "Post it in Engineering" only works if that resolves to one
// room. Its project is the rest of that name, so the same title in two
// projects is two names, and the duplicate this refuses is still nearly always
// an accident. Ad-hoc rooms are exempt: their title describes who is in them
// rather than naming them.
func (m *Messaging) nameIsFree(title, kind, project string) error {
	existing, err := m.store.RoomNamed(title, timeline.Admin, kind, project)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	what := "A permanent room"
	if kind == timeline.ChannelKind {
		what = "A channel"
	}
	where := "under no project"
	if named, err := m.store.Project(project); err == nil && named != nil {
		where = "in " + named.Name
	}
	return refuse("%s called '%s' already exists %s", what, title, where)
}

// SetState opens or closes a room. Closing says the work in it is done; it is
// not deleting and not archiving, and the room stays readable to its audience.
//
// Who may close is who may change the room's other administrative facts: an
// admin for an admin-founded room, any participant for a user-founded one.
func (m *Messaging) SetState(
	username string, isAdmin bool, roomID, state string,
) (*RoomReply, error) {
	if state != timeline.StateOpen && state != timeline.StateClosed {
		return nil, refuse("A state is %q or %q", timeline.StateOpen, timeline.StateClosed)
	}
	room, err := m.requireRoom(roomID, "")
	if err != nil {
		return nil, err
	}
	if err := m.requireInviteAuthority(room, username, isAdmin); err != nil {
		return nil, err
	}
	// A transient room ends by emptying, and its grace period is the only thing
	// that decides when. Closing one would name a second, contradictory end.
	if room.Retention == timeline.Transient {
		return nil, refuse("A transient room is not closed; it ends when everyone leaves")
	}

	if room.State != state {
		if _, err := m.store.SetState(roomID, state); err != nil {
			return nil, err
		}
		what := "closed"
		if state == timeline.StateOpen {
			what = "reopened"
		}
		m.PostEventQuietly(roomID, fmt.Sprintf("%s %s this room", username, what), nil)
	}
	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, nil)
	return &RoomReply{Ok: true, Room: updated}, nil
}

// DissolveProject removes a project. Its rooms survive, filed under none: a
// project holds no messages, so there is nothing in it to lose.
func (m *Messaging) DissolveProject(isAdmin bool, projectID string) (*Ok, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	project, err := m.store.Project(projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, refuse("No such project: %s", projectID)
	}

	freed, err := m.store.RoomsIn(projectID)
	if err != nil {
		return nil, err
	}
	if _, err := m.store.DeleteProject(projectID); err != nil {
		return nil, err
	}
	m.deliver(m.roster(), projectGoneEvent{Type: ProjectGonePush, Project: projectID})
	for _, roomID := range freed {
		if updated, err := m.store.Room(roomID); err == nil && updated != nil {
			m.announceRoom(updated, nil)
		}
	}
	return &Ok{Ok: true}, nil
}

// -- channels ----------------------------------------------------------------

// EnsureChannel declares a channel. Idempotent, so it can run on every boot.
func (m *Messaging) EnsureChannel(channelID, title string, subscribers []string) (*timeline.Room, error) {
	if _, err := m.store.CreateRoom(
		title, "system", timeline.ChannelKind, timeline.Admin, timeline.Persisted, channelID, nil,
		timeline.Filing{},
	); err != nil {
		return nil, err
	}
	for _, username := range subscribers {
		if _, err := m.store.Subscribe(channelID, username); err != nil {
			return nil, err
		}
	}
	return m.store.Room(channelID)
}

// CreateChannel founds a channel. Administrators only, and its audience rule may
// be set at once.
//
// Nothing is pushed: a new channel has no subscribers, so there is nobody to
// tell. The host announces it on `system` instead, which is how a user learns
// there is something to subscribe to -- and the host is where the id of the
// machine channel lives.
func (m *Messaging) CreateChannel(
	username string, isAdmin bool, title string, groups []any, project string,
) (*timeline.Room, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, refuse("A channel needs a name")
	}
	if err := checkName(title); err != nil {
		return nil, err
	}

	filed, err := m.filing(project, "", "")
	if err != nil {
		return nil, err
	}
	if err := m.nameIsFree(title, timeline.ChannelKind, filed.Project); err != nil {
		return nil, err
	}

	admitted := make([]string, 0, len(groups))
	for _, value := range groups {
		groupID, ok := value.(string)
		if !ok {
			return nil, refuse("Not a group: %v", value)
		}
		group, err := m.store.Group(groupID)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, refuse("No such group: %s", groupID)
		}
		admitted = append(admitted, groupID)
	}

	channel, err := m.store.CreateRoom(
		title, username, timeline.ChannelKind, timeline.Admin, timeline.Persisted, "", nil, filed,
	)
	if err != nil {
		return nil, err
	}
	for _, groupID := range admitted {
		if _, err := m.store.AdmitGroup(channel.ID, groupID); err != nil {
			return nil, err
		}
	}
	return m.store.Room(channel.ID)
}

// PublishMessage is a producer's own words on a channel: an administrator's, or
// one of the channel's moderators'.
//
// The producer's path, and separate from Send: a channel is read-only to its
// audience, so the operation that writes to one is not the operation a
// participant uses in a room.
func (m *Messaging) PublishMessage(
	username string, isAdmin bool, channelID, subject, body string,
) (*SendReply, error) {
	channel, err := m.store.Room(channelID)
	if err != nil {
		return nil, err
	}
	// Moderators widen the set of producers rather than defining it. Where there
	// are none the administrator is the only one, refused exactly as before.
	if !isAdmin {
		if channel == nil || len(channel.Moderators) == 0 {
			return nil, refuse("Only an administrator may do that")
		}
		if requireModerator(channel, username) != nil {
			return nil, refuse("Only an administrator or a moderator may do that")
		}
	}
	if channel == nil || channel.Kind != timeline.ChannelKind {
		return nil, refuse("No such room: %s", channelID)
	}
	subject, body, err = content(subject, body)
	if err != nil {
		return nil, err
	}

	message, err := m.store.Append(channelID, username, subject, body, timeline.Text)
	if err != nil {
		return nil, err
	}
	m.deliver(channel.Audience, messageEvent{Type: MessagePush, Message: message})
	return &SendReply{Ok: true, Seq: message.Seq}, nil
}

// Subscribe joins a channel's audience: the one thing a user chooses alone.
func (m *Messaging) Subscribe(username, channelID string) (*timeline.Room, error) {
	if _, err := m.requireRoom(channelID, timeline.ChannelKind); err != nil {
		return nil, err
	}
	eligible, err := m.store.Eligible(channelID, username)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, refuse("That channel is restricted")
	}
	if _, err := m.store.Subscribe(channelID, username); err != nil {
		return nil, err
	}
	channel, err := m.store.Room(channelID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(channel, nil)
	return channel, nil
}

func (m *Messaging) Unsubscribe(username, channelID string) (*Ok, error) {
	if _, err := m.requireRoom(channelID, timeline.ChannelKind); err != nil {
		return nil, err
	}
	if _, err := m.store.Unsubscribe(channelID, username); err != nil {
		return nil, err
	}
	m.deliver([]string{username}, roomGoneEvent{Type: RoomGonePush, Room: channelID})
	return &Ok{Ok: true}, nil
}

// Admit adds a group to a channel's audience, restricting it if it was open.
func (m *Messaging) Admit(isAdmin bool, channelID, groupID string) (*ChannelReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	channel, err := m.requireRoom(channelID, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}
	group, err := m.store.Group(groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, refuse("No such group: %s", groupID)
	}

	// Everyone the channel reached before, so that the people an admission
	// excludes -- everyone, when an open channel is restricted for the first
	// time -- are told by the same call that excluded them.
	reached := channel.Audience
	if _, err := m.store.AdmitGroup(channelID, groupID); err != nil {
		return nil, err
	}
	return m.audienceChanged(channelID, reached)
}

// Revoke withdraws a group. Withdrawing the last leaves the channel open.
func (m *Messaging) Revoke(isAdmin bool, channelID, groupID string) (*ChannelReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	channel, err := m.requireRoom(channelID, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}

	reached := channel.Audience
	if _, err := m.store.RevokeGroup(channelID, groupID); err != nil {
		return nil, err
	}
	return m.audienceChanged(channelID, reached)
}

// audienceChanged announces a channel whose rule moved, and drops whoever it now
// excludes.
//
// A subscription outlives the eligibility that allowed it, so the people who
// lost it keep their row and are simply no longer in the audience. Their client
// is told once, here; nothing else would tell it, because the next message will
// not be addressed to them.
func (m *Messaging) audienceChanged(channelID string, reached []string) (*ChannelReply, error) {
	channel, err := m.store.Room(channelID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(channel, reached)

	keeping := map[string]bool{}
	for _, username := range channel.Audience {
		keeping[username] = true
	}
	for _, username := range reached {
		if !keeping[username] {
			m.deliver([]string{username}, roomGoneEvent{Type: RoomGonePush, Room: channelID})
		}
	}
	return &ChannelReply{Ok: true, Channel: channel}, nil
}

// -- moderation --------------------------------------------------------------

// dismissedQueue is the comment on a submission rejected because the last
// moderator went and nobody was left who could decide it.
const dismissedQueue = "That channel no longer accepts submissions"

// Appoint makes a user a moderator of a channel. Administrators only.
//
// The first appointment is what makes a channel accept submissions, so the
// channel is announced and its subscribers' clients learn they may submit.
func (m *Messaging) Appoint(isAdmin bool, channelID, username string) (*ChannelReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	if _, err := m.requireRoom(channelID, timeline.ChannelKind); err != nil {
		return nil, err
	}
	if !m.known(username) {
		return nil, refuse("No such user: %s", username)
	}
	if _, err := m.store.Appoint(channelID, username); err != nil {
		return nil, err
	}
	channel, err := m.store.Room(channelID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(channel, nil)
	return &ChannelReply{Ok: true, Channel: channel}, nil
}

// Dismiss removes a moderator. Administrators only.
//
// Dismissing the last one leaves a channel that accepts no submissions, and the
// queue has nobody left who may decide it. It is rejected rather than left, so
// each author is told.
func (m *Messaging) Dismiss(isAdmin bool, channelID, username string) (*ChannelReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	if _, err := m.requireRoom(channelID, timeline.ChannelKind); err != nil {
		return nil, err
	}
	if _, err := m.store.Dismiss(channelID, username); err != nil {
		return nil, err
	}
	channel, err := m.store.Room(channelID)
	if err != nil {
		return nil, err
	}
	if len(channel.Moderators) == 0 {
		rejected, err := m.store.RejectAll(channelID, dismissedQueue)
		if err != nil {
			return nil, err
		}
		for index := range rejected {
			m.announceSubmission(&rejected[index], nil)
		}
	}
	m.announceRoom(channel, nil)
	return &ChannelReply{Ok: true, Channel: channel}, nil
}

// Submit proposes a message to a channel. It reaches only the moderators until
// one approves it, and takes no sequence number until then: a rejected one
// would otherwise leave every subscriber a gap to re-request forever.
func (m *Messaging) Submit(username, channelID, subject, body string) (*timeline.Submission, error) {
	channel, err := m.requireAccess(channelID, username, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}
	if len(channel.Moderators) == 0 {
		return nil, refuse("That channel accepts no submissions")
	}
	subject, body, err = content(subject, body)
	if err != nil {
		return nil, err
	}

	submission, err := m.store.Submit(channelID, username, timeline.Subject(subject, body), body)
	if err != nil {
		return nil, err
	}
	if submission == nil {
		// The last moderator was dismissed after the check above.
		return nil, refuse("That channel accepts no submissions")
	}
	m.deliver(channel.Moderators, submissionEvent{Type: SubmissionPush, Submission: submission})
	return submission, nil
}

// Queue is what a channel's moderators have to decide, oldest first.
func (m *Messaging) Queue(username, channelID string) (*QueueReply, error) {
	channel, err := m.requireRoom(channelID, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}
	if err := requireModerator(channel, username); err != nil {
		return nil, err
	}
	queue, err := m.store.Queue(channelID)
	if err != nil {
		return nil, err
	}
	return &QueueReply{Channel: channelID, Submissions: queue}, nil
}

// Approve publishes a submission as its author wrote it and under their name.
// A moderator may not edit one, so attribution is a fact rather than a rule.
func (m *Messaging) Approve(username, submissionID string) (*SendReply, error) {
	submission, channel, err := m.pendingFor(username, submissionID)
	if err != nil {
		return nil, err
	}
	message, err := m.store.Approve(submissionID)
	if errors.Is(err, timeline.ErrNotPending) {
		return nil, refuse("That submission has been decided")
	}
	if err != nil {
		return nil, err
	}
	m.deliver(channel.Audience, messageEvent{Type: MessagePush, Message: message})
	submission.State = timeline.Approved
	m.announceSubmission(submission, channel.Moderators)
	return &SendReply{Ok: true, Seq: message.Seq}, nil
}

// Reject turns a submission down, with a comment if the moderator gives one.
//
// The row stays until its author acknowledges it, so an author who is offline
// now learns the outcome on their next sync rather than never.
func (m *Messaging) Reject(username, submissionID, comment string) (*Ok, error) {
	submission, channel, err := m.pendingFor(username, submissionID)
	if err != nil {
		return nil, err
	}
	var note *string
	if err := checkBody(comment); err != nil {
		return nil, err
	}
	if comment = strings.TrimSpace(comment); comment != "" {
		note = &comment
	}
	decided, err := m.store.Reject(submissionID, note)
	if err != nil {
		return nil, err
	}
	if !decided {
		return nil, refuse("That submission has been decided")
	}
	submission.State, submission.Comment = timeline.Rejected, note
	m.announceSubmission(submission, channel.Moderators)
	return &Ok{Ok: true}, nil
}

// Acknowledge is an author closing a rejection they have seen, which is what
// deletes it.
func (m *Messaging) Acknowledge(username, submissionID string) (*Ok, error) {
	submission, err := m.store.Submission(submissionID)
	if err != nil {
		return nil, err
	}
	// Someone else's reads as absent: acknowledging is the author's act alone.
	if submission == nil || submission.Author != username {
		return nil, refuse("No such submission: %s", submissionID)
	}
	if submission.State == timeline.Pending {
		return nil, refuse("That submission is still pending")
	}
	if _, err := m.store.Acknowledge(submissionID); err != nil {
		return nil, err
	}
	return &Ok{Ok: true}, nil
}

// pendingFor returns a submission and its channel, if this user moderates it and
// nobody has decided it yet.
func (m *Messaging) pendingFor(
	username, submissionID string,
) (*timeline.Submission, *timeline.Room, error) {
	submission, err := m.store.Submission(submissionID)
	if err != nil {
		return nil, nil, err
	}
	if submission == nil {
		return nil, nil, refuse("No such submission: %s", submissionID)
	}
	channel, err := m.requireRoom(submission.Channel, timeline.ChannelKind)
	if err != nil {
		return nil, nil, err
	}
	if err := requireModerator(channel, username); err != nil {
		return nil, nil, err
	}
	if submission.State != timeline.Pending {
		return nil, nil, refuse("That submission has been decided")
	}
	return submission, channel, nil
}

// requireModerator refuses anyone the channel has not appointed, administrators
// included. Whether a channel takes submissions is whether it has moderators, so
// an implicit one would make every channel take them.
func requireModerator(channel *timeline.Room, username string) error {
	for _, moderator := range channel.Moderators {
		if moderator == username {
			return nil
		}
	}
	return refuse("Only a moderator may do that")
}

// announceSubmission tells a submission's author where it stands, and the
// moderators, whose queues drop it.
func (m *Messaging) announceSubmission(submission *timeline.Submission, moderators []string) {
	m.deliver(union([]string{submission.Author}, moderators),
		submissionEvent{Type: SubmissionPush, Submission: submission})
}

// content checks a channel message's subject and body. A given subject has a
// limit, and one of the two must say something.
func content(subject, body string) (string, string, error) {
	subject, body = strings.TrimSpace(subject), strings.TrimSpace(body)
	if len([]rune(subject)) > timeline.SubjectLimit {
		return "", "", refuse("A subject is at most %d characters", timeline.SubjectLimit)
	}
	if subject == "" && body == "" {
		return "", "", refuse("Empty message")
	}
	return subject, body, checkBody(body)
}

// -- opening -----------------------------------------------------------------

// Open marks one channel item opened for this subscriber, and tells their other
// connections, so that every device agrees.
func (m *Messaging) Open(username, channelID string, seq int64) (*OpenReply, error) {
	if _, err := m.requireAccess(channelID, username, timeline.ChannelKind); err != nil {
		return nil, err
	}
	live, err := m.store.HasMessage(channelID, seq)
	if err != nil {
		return nil, err
	}
	if !live {
		return nil, refuse("No such message: %d", seq)
	}
	if err := m.store.MarkOpened(channelID, username, seq); err != nil {
		return nil, err
	}
	m.deliver([]string{username}, openedEvent{Type: OpenedPush, Channel: channelID, Seq: seq})
	return &OpenReply{Ok: true, Channel: channelID, Seq: seq}, nil
}

// -- archival ----------------------------------------------------------------

// SetArchive sets how long a permanent room's or a channel's messages stay live,
// and whether its audience may search them once archived. Administrators only.
func (m *Messaging) SetArchive(isAdmin bool, roomID string, change ArchiveChange) (*RoomReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	room, err := m.requireRoom(roomID, "")
	if err != nil {
		return nil, err
	}
	// A user-founded room keeps everything: retention there is chat-concepts.md
	// section 4's open question, not a setting.
	if room.Kind == timeline.RoomKind && room.Authority != timeline.Admin {
		return nil, refuse("Only a permanent room or a channel is archived")
	}

	archive := room.Archive
	if change.PeriodGiven {
		switch period := change.Period.(type) {
		case nil:
			archive.Period = nil
		case float64:
			if period <= 0 {
				return nil, refuse("A period is a positive number of seconds")
			}
			archive.Period = &period
		default:
			return nil, refuse("A period is a positive number of seconds")
		}
	}
	if change.SearchableGiven {
		searchable, ok := change.Searchable.(bool)
		if !ok {
			return nil, refuse("Searchable is true or false")
		}
		archive.Searchable = searchable
	}

	if err := m.store.SetArchive(roomID, archive); err != nil {
		return nil, err
	}
	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, nil)
	return &RoomReply{Ok: true, Room: updated}, nil
}

// ArchiveRead is the administrator's walk through an archive, a page at a time.
func (m *Messaging) ArchiveRead(isAdmin bool, roomID string, after int64) (*ArchiveReadReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	if _, err := m.requireRoom(roomID, ""); err != nil {
		return nil, err
	}
	messages, more, err := m.store.Archived(roomID, after)
	if err != nil {
		return nil, err
	}
	return &ArchiveReadReply{Room: roomID, After: after, Messages: messages, More: more}, nil
}

// ArchiveSearch finds archived messages. Administrators may always; the room's
// audience may where the administrator enabled it.
func (m *Messaging) ArchiveSearch(
	username string, isAdmin bool, roomID, query string,
) (*ArchiveSearchReply, error) {
	room, err := m.requireRoom(roomID, "")
	if err != nil {
		return nil, err
	}
	if !isAdmin {
		if _, err := m.requireAccess(roomID, username, ""); err != nil {
			return nil, err
		}
		if !room.Archive.Searchable {
			return nil, refuse("That archive is not searchable")
		}
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, refuse("Empty query")
	}
	messages, err := m.store.SearchArchive(roomID, query, ArchiveSearchLimit)
	if err != nil {
		return nil, err
	}
	return &ArchiveSearchReply{Room: roomID, Query: query, Messages: messages}, nil
}

// -- events and announcements ------------------------------------------------

// PostEvent appends a machine event to a room or channel and pushes it. In a
// channel its line becomes its subject, by the rule a publisher's message follows.
func (m *Messaging) PostEvent(roomID, text string, audience []string) (timeline.Message, error) {
	message, err := m.store.Append(roomID, "system", "", text, timeline.Event)
	if err != nil {
		return message, err
	}
	m.deliver(audience, messageEvent{Type: MessagePush, Message: message})
	return message, nil
}

// PostEventQuietly is PostEvent with the failure swallowed, for events raised as
// a side effect of something else: a channel must never be able to fail the work
// that produced it.
func (m *Messaging) PostEventQuietly(roomID, text string, audience []string) {
	_, _ = m.PostEvent(roomID, text, audience)
}

// AnnouncePresence tells everyone else that someone arrived or left. Not the
// subject: presence is a fact about a person rather than a connection, and a
// client that just connected or is closing knows it already.
func (m *Messaging) AnnouncePresence(username string, online bool) {
	roster := m.roster()
	audience := make([]string, 0, len(roster))
	for _, name := range roster {
		if name != username {
			audience = append(audience, name)
		}
	}
	m.deliver(audience, presenceEvent{Type: PresencePush, Username: username, Online: online})
}

// announceRoom tells a room's audience that its shape changed.
//
// `also` names anyone who has just stopped being in the audience and must still
// hear about it.
func (m *Messaging) announceRoom(room *timeline.Room, also []string) {
	if room == nil {
		return
	}
	m.deliver(union(room.Audience, also), roomEvent{Type: RoomPush, Room: room})
}

func (m *Messaging) announceProject(project *timeline.Project) {
	m.deliver(m.roster(), projectEvent{Type: ProjectPush, Project: project})
}

func (m *Messaging) announceGroup(group *timeline.Group) {
	if group == nil {
		return
	}
	m.deliver(m.roster(), groupEvent{Type: GroupPush, Group: group})
}

func union(first, second []string) []string {
	if len(second) == 0 {
		return first
	}
	seen := map[string]bool{}
	joined := make([]string, 0, len(first)+len(second))
	for _, name := range append(append([]string{}, first...), second...) {
		if !seen[name] {
			seen[name] = true
			joined = append(joined, name)
		}
	}
	return joined
}

// repr renders a rejected value the way the error message quotes it.
func repr(value any) string {
	if text, ok := value.(string); ok {
		return "'" + text + "'"
	}
	return fmt.Sprintf("%v", value)
}
