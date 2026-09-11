package client

// The chat protocol over the socket.
//
// Every request rides ApplicationMessage with a pid the server quotes back; a
// frame whose pid is null answers nothing and is a push. A push may be lost,
// duplicated or reordered, and the per-room sequence number is what repairs it:
// a gap between the cursor and what just arrived is closed by asking for the
// difference. One mechanism covers a dropped frame and a reconnect alike.
//
// Three goroutines meet here:
//
//   - the reader, owned by the socket, which only resolves a pending reply or
//     queues a push. Every reply arrives through it, so it must never block.
//   - the applier, which drains that queue. Repairing a gap makes a request, and
//     a request waits on the reader; doing it there would deadlock.
//   - the caller, usually the interface, which makes requests and reads state
//     under the same mutex the applier writes it with.

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"
)

// Application is the package name operations are addressed to.
const Application = "Chat"

// How long a request waits for its reply. The ceiling turns a lost frame into an
// error rather than a hang.
const requestTimeout = 10 * time.Second

// Messages kept per room. A terminal shows a screenful; this is scrollback.
const logLimit = 1000

// ChatError is a refusal from the server, or a request that never came back.
type ChatError struct{ Message string }

func (e *ChatError) Error() string { return e.Message }

// Principal is who a grant admits: a user, or a whole group.
type Principal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Room is a room or a channel; the two share one shape.
type Room struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Kind         string      `json:"kind"`
	Authority    string      `json:"authority"`
	Retention    string      `json:"retention"`
	CreatedBy    string      `json:"createdBy"`
	CreatedAt    float64     `json:"createdAt"`
	Grants       []Principal `json:"grants"`
	RestrictedTo []string    `json:"restrictedTo"`
	Moderators   []string    `json:"moderators"`
	Audience     []string    `json:"audience"`
	Occupants    []string    `json:"occupants"`
	LastSeq      int64       `json:"lastSeq"`
	Archive      Archive     `json:"archive"`
}

// Archive is when a room's messages leave it, and whether its audience may
// search them afterwards. A nil period is never.
type Archive struct {
	Period     *float64 `json:"period"`
	Searchable bool     `json:"searchable"`
}

// ArchiveChange is what an archive.set asks for. An unset field is kept:
// Period counts only when SetPeriod is true, and nil then means never.
type ArchiveChange struct {
	SetPeriod  bool
	Period     *float64
	Searchable *bool
}

type Message struct {
	Room   string `json:"room"`
	Seq    int64  `json:"seq"`
	Author string `json:"author"`
	Kind   string `json:"kind"`
	// A channel message's headline, and nil in a room.
	Subject *string `json:"subject"`
	Body    string  `json:"body"`
	At      float64 `json:"at"`
}

type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type User struct {
	Username string `json:"username"`
	Online   bool   `json:"online"`
}

// Submission is a message proposed to a channel. State "approved" only ever
// arrives on a push, because approval turns it into a message.
type Submission struct {
	ID      string  `json:"id"`
	Channel string  `json:"channel"`
	Author  string  `json:"author"`
	Subject string  `json:"subject"`
	Body    string  `json:"body"`
	At      float64 `json:"at"`
	State   string  `json:"state"`
	Comment *string `json:"comment"`
}

type SyncReply struct {
	Me          string           `json:"me"`
	IsAdmin     bool             `json:"isAdmin"`
	Users       []User           `json:"users"`
	Groups      []Group          `json:"groups"`
	Rooms       []Room           `json:"rooms"`
	Channels    []Room           `json:"channels"`
	Read        map[string]int64 `json:"read"`
	Submissions []Submission     `json:"submissions"`
}

// Client is protocol state: who exists, what spaces there are, and what was said.
type Client struct {
	socket *Socket

	mutex     sync.Mutex
	me        string
	isAdmin   bool
	users     []User
	groups    []Group
	rooms     map[string]Room
	channels  map[string]Room
	log       map[string][]Message
	read      map[string]int64
	connected bool

	// This user's own submissions until closed, and those awaiting their
	// decision as a moderator.
	submissions map[string]Submission
	queued      map[string]Submission

	// Highest sequence applied per room: what a gap is measured against, and a
	// different fact from read.
	cursors   map[string]int64
	repairing map[string]bool

	// Which items this user has opened, per channel: a channel's read state, in
	// place of the read cursor a room keeps.
	opened map[string]map[int64]bool

	// Occupancies this connection holds. The server may release one when the user
	// enters a room on another connection, and says so with an exited push.
	occupancies map[string]bool

	pid     int64
	pending map[int64]chan json.RawMessage

	pushes   []json.RawMessage
	wake     chan struct{}
	stopping chan struct{}
	stopOnce sync.Once

	onChange func()
	onNotice func(string)
}

func NewClient(socket *Socket) *Client {
	c := &Client{
		socket:      socket,
		rooms:       map[string]Room{},
		channels:    map[string]Room{},
		log:         map[string][]Message{},
		read:        map[string]int64{},
		submissions: map[string]Submission{},
		queued:      map[string]Submission{},
		cursors:     map[string]int64{},
		repairing:   map[string]bool{},
		opened:      map[string]map[int64]bool{},
		occupancies: map[string]bool{},
		pending:     map[int64]chan json.RawMessage{},
		wake:        make(chan struct{}, 1),
		stopping:    make(chan struct{}),
		onChange:    func() {},
		onNotice:    func(string) {},
	}
	socket.OnFrame, socket.OnClose = c.onFrame, c.failPending
	go c.applyPushes()
	return c
}

// SetHandlers installs what to call when anything a view draws has changed, and
// with a line worth telling the user. Both run on whichever goroutine noticed.
func (c *Client) SetHandlers(onChange func(), onNotice func(string)) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.onChange, c.onNotice = onChange, onNotice
}

func (c *Client) changed() {
	c.mutex.Lock()
	callback := c.onChange
	c.mutex.Unlock()
	callback()
}

func (c *Client) notice(text string) {
	c.mutex.Lock()
	callback := c.onNotice
	c.mutex.Unlock()
	callback(text)
}

// -- requests ----------------------------------------------------------------

// request is one round trip, returning the reply or a ChatError.
func (c *Client) request(op string, body map[string]any) (json.RawMessage, error) {
	c.mutex.Lock()
	c.pid++
	pid := c.pid
	slot := make(chan json.RawMessage, 1)
	c.pending[pid] = slot
	c.mutex.Unlock()

	args := map[string]any{"op": op}
	for key, value := range body {
		args[key] = value
	}
	err := c.socket.Send(ApplicationMessage, map[string]any{
		"pid": pid, "name": Application, "args": []any{args},
	})
	if err != nil {
		c.abandon(pid)
		return nil, &ChatError{err.Error()}
	}

	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	var reply json.RawMessage
	select {
	case reply = <-slot:
	case <-timer.C:
		c.abandon(pid)
		return nil, &ChatError{"No reply to " + op}
	}

	var refusal struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal(reply, &refusal) == nil && refusal.Error != nil {
		return nil, &ChatError{*refusal.Error}
	}
	return reply, nil
}

// call is request, with the reply decoded into out when out is not nil.
func (c *Client) call(op string, body map[string]any, out any) error {
	reply, err := c.request(op, body)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(reply, out); err != nil {
		return &ChatError{fmt.Sprintf("Unreadable reply to %s: %v", op, err)}
	}
	return nil
}

func (c *Client) abandon(pid int64) {
	c.mutex.Lock()
	delete(c.pending, pid)
	c.mutex.Unlock()
}

// -- frames ------------------------------------------------------------------

// onFrame runs on the reader. Fast paths only: never block here.
func (c *Client) onFrame(frame Frame) {
	if frame.Name != ApplicationMessage || len(frame.Params) == 0 {
		return
	}
	var envelope struct {
		Pid  json.RawMessage   `json:"pid"`
		Args []json.RawMessage `json:"args"`
	}
	if json.Unmarshal(frame.Params[0], &envelope) != nil {
		return
	}

	if len(envelope.Pid) == 0 || string(envelope.Pid) == "null" {
		if len(envelope.Args) > 0 {
			c.mutex.Lock()
			c.pushes = append(c.pushes, envelope.Args[0])
			c.mutex.Unlock()
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
		return
	}

	var pid int64
	if json.Unmarshal(envelope.Pid, &pid) != nil {
		return
	}
	c.mutex.Lock()
	slot, ok := c.pending[pid]
	delete(c.pending, pid)
	c.mutex.Unlock()
	if ok {
		reply := json.RawMessage("null")
		if len(envelope.Args) > 0 {
			reply = envelope.Args[0]
		}
		slot <- reply
	}
}

func (c *Client) failPending() {
	c.mutex.Lock()
	c.connected = false
	pending := c.pending
	c.pending = map[int64]chan json.RawMessage{}
	c.mutex.Unlock()
	for _, slot := range pending {
		slot <- json.RawMessage(`{"error":"Disconnected"}`)
	}
	c.changed()
}

func (c *Client) applyPushes() {
	for {
		select {
		case <-c.stopping:
			return
		case <-c.wake:
		}
		for {
			c.mutex.Lock()
			if len(c.pushes) == 0 {
				c.mutex.Unlock()
				break
			}
			event := c.pushes[0]
			c.pushes = c.pushes[1:]
			c.mutex.Unlock()
			c.dispatch(event)
		}
	}
}

// dispatch applies one push. One that cannot be decoded is dropped; the next
// message through re-detects any gap it left behind.
func (c *Client) dispatch(raw json.RawMessage) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return
	}

	switch head.Type {
	case "message":
		var message Message
		if json.Unmarshal(raw, &message) == nil {
			c.apply(message)
		}
	case "room":
		var event struct {
			Room *Room `json:"room"`
		}
		if json.Unmarshal(raw, &event) == nil && event.Room != nil {
			c.track(*event.Room)
		}
		c.changed()
	case "roomGone":
		var event struct {
			Room string `json:"room"`
		}
		if json.Unmarshal(raw, &event) == nil {
			c.forget(event.Room)
		}
		c.changed()
	case "presence":
		var event User
		if json.Unmarshal(raw, &event) == nil {
			c.mutex.Lock()
			for index := range c.users {
				if c.users[index].Username == event.Username {
					c.users[index].Online = event.Online
				}
			}
			c.mutex.Unlock()
		}
		c.changed()
	case "group":
		var event struct {
			Group *Group `json:"group"`
		}
		if json.Unmarshal(raw, &event) == nil && event.Group != nil {
			c.trackGroup(*event.Group)
		}
		c.changed()
	case "submission":
		var event struct {
			Submission *Submission `json:"submission"`
		}
		if json.Unmarshal(raw, &event) == nil && event.Submission != nil {
			c.trackSubmission(*event.Submission, true)
		}
		c.changed()
	case "opened":
		var event struct {
			Channel string `json:"channel"`
			Seq     int64  `json:"seq"`
		}
		if json.Unmarshal(raw, &event) == nil {
			c.mutex.Lock()
			c.markOpenedLocked(event.Channel, event.Seq)
			c.mutex.Unlock()
		}
		c.changed()
	case "archived":
		var event struct {
			Room    string `json:"room"`
			Through int64  `json:"through"`
		}
		if json.Unmarshal(raw, &event) == nil {
			c.dropThrough(event.Room, event.Through)
		}
		c.changed()
	case "exited":
		var event struct {
			Occupancy string `json:"occupancy"`
		}
		if json.Unmarshal(raw, &event) == nil {
			c.mutex.Lock()
			delete(c.occupancies, event.Occupancy)
			c.mutex.Unlock()
		}
		c.changed()
	}
}

// dropThrough forgets what the server archived. The cursor stays: a sequence is
// never reissued, so nothing at or below it can arrive again.
func (c *Client) dropThrough(room string, through int64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.log[room] = slices.DeleteFunc(slices.Clone(c.log[room]), func(m Message) bool { return m.Seq <= through })
	for seq := range c.opened[room] {
		if seq <= through {
			delete(c.opened[room], seq)
		}
	}
}

func (c *Client) markOpenedLocked(channel string, seq int64) {
	if c.opened[channel] == nil {
		c.opened[channel] = map[int64]bool{}
	}
	c.opened[channel][seq] = true
}

// -- the sequence contract ---------------------------------------------------

// apply takes one message against its room's cursor: at or below it is seen
// already, one above is the next, and anything higher means something never arrived.
func (c *Client) apply(message Message) {
	c.mutex.Lock()
	cursor := c.cursors[message.Room]
	if message.Seq <= cursor {
		c.mutex.Unlock()
		return
	}
	if message.Seq > cursor+1 {
		// Dropping this copy is safe: the server stored it before publishing,
		// so the backfill about to be requested contains it.
		c.mutex.Unlock()
		c.repair(message.Room)
		return
	}
	c.cursors[message.Room] = message.Seq
	c.appendLocked(message.Room, message)
	// Opened for its author, as the server records it: they wrote it.
	if _, isChannel := c.channels[message.Room]; isChannel && message.Author == c.me && message.Kind == "text" {
		c.markOpenedLocked(message.Room, message.Seq)
	}
	c.mutex.Unlock()
	c.changed()
}

func (c *Client) repair(room string) {
	c.mutex.Lock()
	if c.repairing[room] {
		c.mutex.Unlock()
		return
	}
	c.repairing[room] = true
	before := c.cursors[room]
	c.mutex.Unlock()
	defer func() {
		c.mutex.Lock()
		delete(c.repairing, room)
		c.mutex.Unlock()
	}()

	var reply struct {
		Messages []Message `json:"messages"`
		// On a channel, which of these this user has opened.
		Opened []int64 `json:"opened"`
	}
	if err := c.call("history", map[string]any{"room": room, "since": before}, &reply); err != nil {
		// The cursor stays put, so the next message re-detects the gap.
		return
	}
	messages := reply.Messages

	c.mutex.Lock()
	// A backfill is capped at the tail, and asking again returns the same slice.
	// The cursor must not jump the shortfall in silence, so the gap is marked.
	if len(messages) > 0 && messages[0].Seq > before+1 {
		c.appendLocked(room, Message{
			Room:   room,
			Seq:    messages[0].Seq - 1,
			Author: "system",
			Kind:   "event",
			Body:   fmt.Sprintf("%d earlier message(s) not shown", messages[0].Seq-before-1),
			At:     messages[0].At,
		})
	}
	for _, message := range messages {
		if message.Seq > c.cursors[room] {
			c.cursors[room] = message.Seq
			c.appendLocked(room, message)
		}
	}
	for _, seq := range reply.Opened {
		c.markOpenedLocked(room, seq)
	}
	c.mutex.Unlock()
	c.changed()
}

func (c *Client) appendLocked(room string, message Message) {
	log := append(c.log[room], message)
	if len(log) > logLimit {
		log = slices.Clone(log[len(log)-logLimit:])
	}
	c.log[room] = log

	// A room is announced when its shape changes, not when it is spoken in, so
	// its lastSeq would otherwise lag and every unread count with it.
	for _, spaces := range []map[string]Room{c.rooms, c.channels} {
		if space, ok := spaces[room]; ok {
			space.LastSeq = max(space.LastSeq, message.Seq)
			spaces[room] = space
		}
	}
}

// -- operations --------------------------------------------------------------

func (c *Client) Sync() (*SyncReply, error) {
	var reply SyncReply
	if err := c.call("sync", nil, &reply); err != nil {
		return nil, err
	}

	c.mutex.Lock()
	c.me, c.isAdmin = reply.Me, reply.IsAdmin
	c.users, c.groups = reply.Users, reply.Groups
	c.rooms, c.channels = map[string]Room{}, map[string]Room{}
	for _, room := range reply.Rooms {
		c.rooms[room.ID] = room
	}
	for _, channel := range reply.Channels {
		c.channels[channel.ID] = channel
	}
	c.read = map[string]int64{}
	for room, seq := range reply.Read {
		c.read[room] = seq
	}
	c.submissions = map[string]Submission{}
	for _, submission := range reply.Submissions {
		c.submissions[submission.ID] = submission
	}
	c.connected = true

	var behind []string
	for _, spaces := range []map[string]Room{c.rooms, c.channels} {
		for id, space := range spaces {
			if space.LastSeq > c.cursors[id] {
				behind = append(behind, id)
			}
		}
	}
	c.mutex.Unlock()

	// Backfill before announcing, so a view does not draw twice for what it missed.
	for _, id := range behind {
		c.repair(id)
	}
	c.changed()
	return &reply, nil
}

func (c *Client) Send(room, body string) error {
	return c.call("send", map[string]any{"room": room, "body": body}, nil)
}

// OpenRoom raises an ad-hoc room. An empty title lets the server derive one.
func (c *Client) OpenRoom(invite []Principal, title, retention string) (Room, error) {
	var room Room
	body := map[string]any{"invite": orEmpty(invite), "title": nil, "retention": retention}
	if title != "" {
		body["title"] = title
	}
	if err := c.call("open", body, &room); err != nil {
		return room, err
	}
	return c.track(room), nil
}

func (c *Client) CreateRoom(title string, invite []Principal) (Room, error) {
	var room Room
	if err := c.call("create", map[string]any{"title": title, "invite": orEmpty(invite)}, &room); err != nil {
		return room, err
	}
	return c.track(room), nil
}

func (c *Client) Invite(room string, principal Principal) (Room, error) {
	return c.grant("invite", room, principal)
}

func (c *Client) Uninvite(room string, principal Principal) (Room, error) {
	return c.grant("uninvite", room, principal)
}

func (c *Client) grant(op, room string, principal Principal) (Room, error) {
	var reply struct {
		Room Room `json:"room"`
	}
	if err := c.call(op, map[string]any{"room": room, "principal": principal}, &reply); err != nil {
		return reply.Room, err
	}
	return c.track(reply.Room), nil
}

func (c *Client) Leave(room string) error {
	if err := c.call("leave", map[string]any{"room": room}, nil); err != nil {
		return err
	}
	c.forget(room)
	return nil
}

// Enter takes a place in a room and returns the occupancy that Exit gives up.
func (c *Client) Enter(room string) (string, error) {
	var reply struct {
		Occupancy string `json:"occupancy"`
	}
	if err := c.call("enter", map[string]any{"room": room}, &reply); err != nil {
		return "", err
	}
	c.mutex.Lock()
	c.occupancies[reply.Occupancy] = true
	c.mutex.Unlock()
	return reply.Occupancy, nil
}

func (c *Client) Exit(occupancy string) error {
	c.mutex.Lock()
	delete(c.occupancies, occupancy)
	c.mutex.Unlock()
	return c.call("exit", map[string]any{"occupancy": occupancy}, nil)
}

// Holds is whether this connection is still in the room an occupancy was taken
// for, rather than released by an entry elsewhere.
func (c *Client) Holds(occupancy string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.occupancies[occupancy]
}

// MarkRead moves the read cursor, which is seen rather than received.
func (c *Client) MarkRead(room string, seq int64) error {
	c.mutex.Lock()
	if seq <= c.read[room] {
		c.mutex.Unlock()
		return nil
	}
	c.read[room] = seq
	c.mutex.Unlock()
	return c.call("read", map[string]any{"room": room, "seq": seq}, nil)
}

func (c *Client) CreateGroup(name string, members []string) (Group, error) {
	var group Group
	if err := c.call("group.create", map[string]any{"name": name, "members": orEmpty(members)}, &group); err != nil {
		return group, err
	}
	return c.trackGroup(group), nil
}

func (c *Client) AssignGroup(group, username string) (Group, error) {
	var reply Group
	err := c.call("group.assign", map[string]any{"group": group, "username": username}, &reply)
	return reply, err
}

func (c *Client) UnassignGroup(group, username string) (Group, error) {
	var reply Group
	err := c.call("group.unassign", map[string]any{"group": group, "username": username}, &reply)
	return reply, err
}

// CreateChannel founds a channel. Founding is not subscribing, so it is not tracked.
func (c *Client) CreateChannel(title string, groups []string) (Room, error) {
	var channel Room
	err := c.call("channel.create", map[string]any{"title": title, "groups": orEmpty(groups)}, &channel)
	return channel, err
}

// Publish writes to a channel. An empty subject lets the server take the body's
// first line.
func (c *Client) Publish(channel, subject, body string) error {
	return c.call("channel.publish",
		map[string]any{"channel": channel, "subject": subject, "body": body}, nil)
}

// Open marks one channel item opened. The server tells this user's other
// connections, so every device agrees.
func (c *Client) Open(channel string, seq int64) error {
	if err := c.call("channel.open", map[string]any{"channel": channel, "seq": seq}, nil); err != nil {
		return err
	}
	c.mutex.Lock()
	c.markOpenedLocked(channel, seq)
	c.mutex.Unlock()
	return nil
}

func (c *Client) AdmitGroup(channel, group string) (Room, error) {
	return c.rule("channel.admit", channel, "group", group)
}

func (c *Client) RevokeGroup(channel, group string) (Room, error) {
	return c.rule("channel.revoke", channel, "group", group)
}

func (c *Client) Appoint(channel, username string) (Room, error) {
	return c.rule("channel.appoint", channel, "username", username)
}

func (c *Client) Dismiss(channel, username string) (Room, error) {
	return c.rule("channel.dismiss", channel, "username", username)
}

// rule is an administrator's change to a channel, answered with the channel.
func (c *Client) rule(op, channel, field, value string) (Room, error) {
	var reply struct {
		Channel Room `json:"channel"`
	}
	err := c.call(op, map[string]any{"channel": channel, field: value}, &reply)
	return reply.Channel, err
}

func (c *Client) Submit(channel, subject, body string) (Submission, error) {
	var submission Submission
	fields := map[string]any{"channel": channel, "subject": subject, "body": body}
	if err := c.call("channel.submit", fields, &submission); err != nil {
		return submission, err
	}
	return c.trackSubmission(submission, false), nil
}

// Queue is what a channel's moderators have to decide, replacing what was known of it.
func (c *Client) Queue(channel string) ([]Submission, error) {
	var reply struct {
		Submissions []Submission `json:"submissions"`
	}
	if err := c.call("channel.queue", map[string]any{"channel": channel}, &reply); err != nil {
		return nil, err
	}
	c.mutex.Lock()
	for id, submission := range c.queued {
		if submission.Channel == channel {
			delete(c.queued, id)
		}
	}
	for _, submission := range reply.Submissions {
		c.queued[submission.ID] = submission
	}
	c.mutex.Unlock()
	return reply.Submissions, nil
}

func (c *Client) Approve(submission string) error {
	if err := c.call("submission.approve", map[string]any{"submission": submission}, nil); err != nil {
		return err
	}
	c.unqueue(submission)
	return nil
}

// Reject decides against a submission. An empty comment is sent as none.
func (c *Client) Reject(submission, comment string) error {
	body := map[string]any{"submission": submission, "comment": nil}
	if comment != "" {
		body["comment"] = comment
	}
	if err := c.call("submission.reject", body, nil); err != nil {
		return err
	}
	c.unqueue(submission)
	return nil
}

func (c *Client) unqueue(submission string) {
	c.mutex.Lock()
	delete(c.queued, submission)
	c.mutex.Unlock()
}

func (c *Client) Acknowledge(submission string) error {
	if err := c.call("submission.acknowledge", map[string]any{"submission": submission}, nil); err != nil {
		return err
	}
	c.mutex.Lock()
	delete(c.submissions, submission)
	c.mutex.Unlock()
	return nil
}

// SetArchive changes when a permanent room's or a channel's messages are
// archived, and whether its audience may search them. Administrators only.
func (c *Client) SetArchive(room string, change ArchiveChange) (Room, error) {
	body := map[string]any{"room": room}
	if change.SetPeriod {
		body["period"] = change.Period
	}
	if change.Searchable != nil {
		body["searchable"] = *change.Searchable
	}
	var reply struct {
		Room Room `json:"room"`
	}
	if err := c.call("archive.set", body, &reply); err != nil {
		return reply.Room, err
	}
	return c.track(reply.Room), nil
}

// ArchiveRead is one page of an archive after `after`, oldest first, and
// whether more follows. Administrators only.
func (c *Client) ArchiveRead(room string, after int64) ([]Message, bool, error) {
	var reply struct {
		Messages []Message `json:"messages"`
		More     bool      `json:"more"`
	}
	err := c.call("archive.read", map[string]any{"room": room, "after": after}, &reply)
	return reply.Messages, reply.More, err
}

// ArchiveSearch is the archived messages whose subject or body contains query,
// newest first.
func (c *Client) ArchiveSearch(room, query string) ([]Message, error) {
	var reply struct {
		Messages []Message `json:"messages"`
	}
	err := c.call("archive.search", map[string]any{"room": room, "query": query}, &reply)
	return reply.Messages, err
}

func (c *Client) Subscribe(channel string) (Room, error) {
	var room Room
	if err := c.call("subscribe", map[string]any{"channel": channel}, &room); err != nil {
		return room, err
	}
	return c.track(room), nil
}

func (c *Client) Unsubscribe(channel string) error {
	if err := c.call("unsubscribe", map[string]any{"channel": channel}, nil); err != nil {
		return err
	}
	c.forget(channel)
	return nil
}

// Stop ends the applier and hangs up. Safe to call twice.
func (c *Client) Stop() {
	c.stopOnce.Do(func() { close(c.stopping) })
	c.socket.Close()
}

// -- state -------------------------------------------------------------------

// trackGroup records a group at once rather than waiting for its push, which a
// caller that creates a group and immediately invites it would race.
func (c *Client) trackGroup(group Group) Group {
	if group.ID == "" {
		return group
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.groups = slices.DeleteFunc(slices.Clone(c.groups), func(g Group) bool { return g.ID == group.ID })
	c.groups = append(c.groups, group)
	sort.SliceStable(c.groups, func(i, j int) bool { return c.groups[i].Name < c.groups[j].Name })
	return group
}

func (c *Client) track(space Room) Room {
	if space.ID == "" {
		return space
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if space.Kind == "channel" {
		c.channels[space.ID] = space
	} else {
		c.rooms[space.ID] = space
	}
	return space
}

// trackSubmission follows a submission into a queue, out of it, and back to its
// author, who keeps a rejection until acknowledging it.
func (c *Client) trackSubmission(submission Submission, announce bool) Submission {
	if submission.ID == "" {
		return submission
	}
	c.mutex.Lock()
	mine := submission.Author == c.me
	if submission.State == "pending" && !mine {
		c.queued[submission.ID] = submission
	} else {
		delete(c.queued, submission.ID)
	}
	if mine && submission.State == "approved" {
		delete(c.submissions, submission.ID)
	} else if mine {
		c.submissions[submission.ID] = submission
	}
	c.mutex.Unlock()

	if announce && (mine || submission.State == "pending") {
		c.notice(c.DescribeSubmission(submission))
	}
	return submission
}

func (c *Client) forget(space string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	delete(c.rooms, space)
	delete(c.channels, space)
	delete(c.log, space)
	delete(c.cursors, space)
	delete(c.read, space)
	delete(c.opened, space)
}

// -- reading state -----------------------------------------------------------
//
// Each returns a copy, so a caller may hold it while the applier moves on.

func (c *Client) Me() string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.me
}

func (c *Client) IsAdmin() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.isAdmin
}

func (c *Client) Connected() bool { return c.socket.Connected() }

func (c *Client) Users() []User {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return slices.Clone(c.users)
}

func (c *Client) Groups() []Group {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return slices.Clone(c.groups)
}

func (c *Client) Rooms() map[string]Room {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return maps.Clone(c.rooms)
}

func (c *Client) Channels() map[string]Room {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return maps.Clone(c.channels)
}

// Space is a room or a channel by id.
func (c *Client) Space(id string) (Room, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if room, ok := c.rooms[id]; ok {
		return room, true
	}
	channel, ok := c.channels[id]
	return channel, ok
}

func (c *Client) Log(space string) []Message {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return slices.Clone(c.log[space])
}

func (c *Client) ReadCursor(space string) int64 {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.read[space]
}

func (c *Client) Submissions() map[string]Submission {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return maps.Clone(c.submissions)
}

func (c *Client) Queued() map[string]Submission {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return maps.Clone(c.queued)
}

func (c *Client) Moderates(space string) bool {
	room, ok := c.Space(space)
	return ok && slices.Contains(room.Moderators, c.Me())
}

// Opened is which of a channel's items this user has opened.
func (c *Client) Opened(channel string) map[int64]bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return maps.Clone(c.opened[channel])
}

// Unread is how far this person is behind in a room: the furthest sequence
// known, received or announced, less the furthest seen. A channel counts as
// zero: its items are opened one at a time, inside it, and counted nowhere else.
func (c *Client) Unread(space string) int64 {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	room, ok := c.rooms[space]
	if !ok {
		return 0
	}
	known := max(room.LastSeq, c.cursors[space])
	return max(0, known-c.read[space])
}

// DescribeSubmission is one line: which, whose, where, and how it stands.
func (c *Client) DescribeSubmission(submission Submission) string {
	where := Prefix(submission.Channel, 8)
	if space, ok := c.Space(submission.Channel); ok {
		where = space.Title
	}
	state := or(submission.State, "?")
	if submission.State == "rejected" && submission.Comment != nil && *submission.Comment != "" {
		state += " (" + *submission.Comment + ")"
	}
	return fmt.Sprintf("[%s] %s -> %s, %s: %s",
		Prefix(submission.ID, 8), or(submission.Author, "?"), where, state, submission.Body)
}

// Prefix is the first n characters of text, or all of it when shorter.
func Prefix(text string, n int) string {
	runes := []rune(text)
	if len(runes) > n {
		return string(runes[:n])
	}
	return text
}

func or(text, fallback string) string {
	if text == "" {
		return fallback
	}
	return text
}

// orEmpty sends an absent list as [] rather than null.
func orEmpty[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// Connect logs in and opens a synced client: the whole handshake in one call.
func Connect(base, username, password string) (*HTTP, *Client, Profile, error) {
	session := NewHTTP(base)
	profile, err := session.Login(username, password)
	if err != nil {
		return nil, nil, profile, err
	}

	socket := NewSocket(session)
	client := NewClient(socket)
	if err := socket.Connect(); err != nil {
		client.Stop()
		return nil, nil, profile, err
	}
	// A reply is matched by pid rather than arrival order, so the handshake
	// frame need not be waited for first.
	if _, err := client.Sync(); err != nil {
		client.Stop()
		return nil, nil, profile, err
	}
	return session, client, profile, nil
}
