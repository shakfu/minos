// Package messaging holds the conversation operations: groups, rooms, channels,
// occupancy and presence.
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
// Unlike the Python server this replaces, delivery is a direct call rather than
// a publish. One process serves every connection, so there is no second process
// to tell and nothing to serialise a message through: `deliver(audience, event)`
// reaches the sockets in the same goroutine that appended the message. The
// sequence number survives that change untouched, because it never repaired the
// bus alone -- it also covers a reconnect, an hour offline and a lagging reader.
package messaging

import (
	"fmt"
	"sort"
	"strings"

	"minos/internal/timeline"
)

// Push types, as seen by a subscriber.
const (
	MessagePush  = "message"
	RoomPush     = "room"
	RoomGonePush = "roomGone"
	PresencePush = "presence"
	GroupPush    = "group"
)

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

type groupEvent struct {
	Type  string          `json:"type"`
	Group *timeline.Group `json:"group"`
}

// -- replies -----------------------------------------------------------------

// UserState is one account and whether it is connected.
type UserState struct {
	Username string `json:"username"`
	Online   bool   `json:"online"`
}

// SyncReply is everything a freshly connected client needs.
type SyncReply struct {
	Me       string           `json:"me"`
	IsAdmin  bool             `json:"isAdmin"`
	Users    []UserState      `json:"users"`
	Groups   []timeline.Group `json:"groups"`
	Rooms    []timeline.Room  `json:"rooms"`
	Channels []timeline.Room  `json:"channels"`
	Read     map[string]int64 `json:"read"`
}

// HistoryReply carries the backfill and where the room actually ends.
type HistoryReply struct {
	Room     string             `json:"room"`
	Since    int64              `json:"since"`
	LastSeq  int64              `json:"lastSeq"`
	Messages []timeline.Message `json:"messages"`
}

// Ok is the shape of an operation that answers nothing else.
type Ok struct {
	Ok bool `json:"ok"`
}

type SendReply struct {
	Ok  bool  `json:"ok"`
	Seq int64 `json:"seq"`
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
	cursors, err := m.store.ReadCursors(username)
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
		Groups: groups, Rooms: rooms, Channels: channels, Read: cursors,
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
	return &HistoryReply{Room: roomID, Since: since, LastSeq: room.LastSeq, Messages: messages}, nil
}

func (m *Messaging) Send(username, roomID, body string) (*SendReply, error) {
	room, err := m.requireAccess(roomID, username, "")
	if err != nil {
		return nil, err
	}
	if room.Kind == timeline.ChannelKind {
		// A channel is read-only to its audience. Submission for approval is a
		// separate feature and deliberately not implemented, so there is nothing
		// for this to fall back to.
		return nil, refuse("A channel is read-only")
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, refuse("Empty message")
	}

	message, err := m.store.Append(roomID, username, body, timeline.Text)
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
	if title == "" {
		title, err = m.deriveTitle(grants)
		if err != nil {
			return nil, err
		}
	}

	room, err := m.store.CreateRoom(
		title, username, timeline.RoomKind, timeline.User, retention, "", grants)
	if err != nil {
		return nil, err
	}
	m.announceRoom(room, nil)
	return room, nil
}

// CreateRoom founds a permanent room. Admins only, and always persisted.
func (m *Messaging) CreateRoom(username string, isAdmin bool, title string, invitees []any) (*timeline.Room, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, refuse("A permanent room needs a name")
	}

	// A permanent room's title is a name: institutional, chosen, and meant to be
	// referred to. "Post it in Engineering" only works if that resolves to one
	// room, so the name is unique among permanent rooms -- and the duplicate
	// this refuses is nearly always an accident. Ad-hoc rooms are exempt: their
	// title describes who is in them rather than naming them.
	existing, err := m.store.RoomNamed(title, timeline.Admin, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, refuse("A permanent room called '%s' already exists", title)
	}

	grants, err := m.grantsFor(username, invitees)
	if err != nil {
		return nil, err
	}

	room, err := m.store.CreateRoom(
		title, username, timeline.RoomKind, timeline.Admin, timeline.Persisted, "", grants)
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

	label := principal.ID
	if principal.Kind == timeline.PrincipalGroup {
		if group, err := m.store.Group(principal.ID); err == nil && group != nil {
			label = "group " + group.Name
		}
	}

	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	if _, err := m.PostEvent(roomID, username+" invited "+label, updated.Audience); err != nil {
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

	// Read before the change: afterwards the person removed is not in the
	// audience, and this is the last moment their client can be told.
	audience := room.Audience

	removed, err := m.store.RemoveGrant(roomID, principal.Kind, principal.ID)
	if err != nil {
		return nil, err
	}
	if !removed {
		return &RoomReply{Ok: true, Room: room}, nil
	}

	if _, err := m.PostEvent(roomID, username+" removed "+principal.ID, audience); err != nil {
		return nil, err
	}
	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, audience)
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
	audience := room.Audience

	removed, err := m.store.RemoveGrant(roomID, timeline.PrincipalUser, username)
	if err != nil {
		return nil, err
	}
	if !removed {
		return nil, refuse("Access to this room comes from a group, so it cannot be left")
	}

	if _, err := m.PostEvent(roomID, username+" left", audience); err != nil {
		return nil, err
	}
	updated, err := m.store.Room(roomID)
	if err != nil {
		return nil, err
	}
	m.announceRoom(updated, audience)
	return &Ok{Ok: true}, nil
}

// -- occupancy ---------------------------------------------------------------

// Enter takes a place in a room. Distinct from being invited to it.
//
// A transient room's life is measured from the moment its last occupant leaves,
// so this is what keeps one alive -- and what rescues one during its grace.
func (m *Messaging) Enter(username, roomID string) (*EnterReply, error) {
	room, err := m.requireAccess(roomID, username, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	occupancy, err := m.store.Enter(roomID, username)
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

// Sweep deletes transient rooms whose grace period has run out.
//
// The audience is read before the room goes, because afterwards there is nobody
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
		if err := m.store.DeleteRoom(roomID); err != nil {
			return gone, err
		}
		m.deliver(audience, roomGoneEvent{Type: RoomGonePush, Room: roomID})
		gone = append(gone, roomID)
	}
	return gone, nil
}

// -- read state --------------------------------------------------------------

func (m *Messaging) MarkRead(username, roomID string, seq int64) (*ReadReply, error) {
	if _, err := m.requireAccess(roomID, username, ""); err != nil {
		return nil, err
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
		if !keeping[room.ID] {
			m.deliver([]string{username}, roomGoneEvent{Type: RoomGonePush, Room: room.ID})
		}
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

// -- channels ----------------------------------------------------------------

// EnsureChannel declares a channel. Idempotent, so it can run on every boot.
func (m *Messaging) EnsureChannel(channelID, title string, subscribers []string) (*timeline.Room, error) {
	if _, err := m.store.CreateRoom(
		title, "system", timeline.ChannelKind, timeline.Admin, timeline.Persisted, channelID, nil,
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
	username string, isAdmin bool, title string, groups []any,
) (*timeline.Room, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, refuse("A channel needs a name")
	}

	// A channel's name is institutional, referred to, and unique among channels
	// for the same reason a permanent room's is among rooms.
	existing, err := m.store.RoomNamed(title, timeline.Admin, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, refuse("A channel called '%s' already exists", title)
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
		title, username, timeline.ChannelKind, timeline.Admin, timeline.Persisted, "", nil,
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

// PublishMessage is an administrator's own words on a channel.
//
// The producer's path, and separate from Send: a channel is read-only to its
// audience, so the operation that writes to one is not the operation a
// participant uses in a room.
func (m *Messaging) PublishMessage(
	username string, isAdmin bool, channelID, body string,
) (*SendReply, error) {
	if err := requireAdmin(isAdmin); err != nil {
		return nil, err
	}
	channel, err := m.requireRoom(channelID, timeline.ChannelKind)
	if err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, refuse("Empty message")
	}

	message, err := m.store.Append(channelID, username, body, timeline.Text)
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

// -- events and announcements ------------------------------------------------

// PostEvent appends a machine event to a room or channel and pushes it.
func (m *Messaging) PostEvent(roomID, text string, audience []string) (timeline.Message, error) {
	message, err := m.store.Append(roomID, "system", text, timeline.Event)
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
