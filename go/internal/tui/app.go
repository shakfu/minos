// Package tui is the terminal interface.
//
// A room is a place, and the interface makes that literal: the room you enter
// is the room you occupy, and while you are in it the screen is that room.
// Looking away leaves it. For a transient room that is what keeps it alive, and
// closing the program is leaving.
//
// Everything the retired desktop expressed by dragging is a command here. The
// sidebar lists what you can enter, the pane shows one conversation, the
// composer takes text or a command, and the status line says whether the socket
// is up -- in a terminal there is nowhere else to notice that it is not.
package tui

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
)

// systemChannel is the machine channel's id, fixed by the wire contract. It is
// a log, with nothing pending and nothing to open.
const systemChannel = "system"

const (
	sidebarWidth = 24
	authorWidth  = 10

	// Lines of command output kept under the conversation, and in memory.
	noticeLines = 6
	noticeKeep  = 200
)

var help = [][2]string{
	{"/help", "this list"},
	{"/rooms  /people  /groups", "list what there is"},
	{"/open <who>...", "raise an ad-hoc room, kept"},
	{"/meet <who>...", "raise an ad-hoc room, discarded when everyone leaves"},
	{"/create <title>", "found a permanent room (admin)"},
	{"/invite <who>", "admit a user, or @group"},
	{"/uninvite <who>", "withdraw a grant"},
	{"/exit", "step out of this room, keeping your place in it"},
	{"/leave", "give up your place in this room"},
	{"/group new <name> [user]...", "create a group (admin)"},
	{"/group add|rm <group> <user>", "assign or unassign (admin)"},
	{"/subscribe <channel>  /unsubscribe", "a channel's audience is your own choice"},
	{"/channel new <title> [@group]...", "found a channel (admin)"},
	{"/channel admit|revoke <channel> <group>", "restrict a channel to groups (admin)"},
	{"/channel appoint|dismiss <channel> <user>", "who moderates a channel (admin)"},
	{"<channel>", "one word: a channel's name with _ for a space, its id, or the start of its id"},
	{"/queue", "what this channel's moderators have to decide"},
	{"/approve <id>  /reject <id> [why]", "decide a submission (moderator)"},
	{"/submissions  /ack <id>", "what you submitted, and closing a rejection"},
	{"/archive [<period>|never] [searchable|private]", "when this space is archived (admin)"},
	{"/archived [<after>]", "read this space's archive (admin)"},
	{"/search <text>", "search this space's archive, where allowed"},
	{"Esc", "close archive results"},
	{"/quit", "leave every room and stop"},
	{"Tab / S-Tab", "next or previous space or person, outside a room"},
	{"Enter on a room", "go in: the screen is that room until you step out"},
	{"Enter on a person", "raise a room with them"},
	{"Up / Down", "move through a channel's items"},
	{"Enter on an item", "open it and show its body, or close it"},
	{"subject | body", "in a channel: a headline, then the text"},
	{"PgUp / PgDn", "scroll this room"},
}

// Ui is the interface's state. Everything but notices belongs to the loop's goroutine.
type Ui struct {
	screen  tcell.Screen
	client  *client.Client
	profile client.Profile

	input     []rune
	scroll    int
	selected  string
	person    string // selected in PEOPLE instead of a space; selected is then ""
	occupancy string

	// In a channel: the item under the cursor, as an index into feedOrder, and
	// the one whose body is shown, by seq.
	item     int
	expanded int64

	// Archive output, shown in place of the conversation until Esc or a move
	// to another space.
	resultsTitle string
	results      []string

	// Rooms seen so far, so a new one is an invitation, and the invitations that
	// arrived while the user was in a room, offered once they step out.
	known map[string]bool
	held  []string

	// A command that would take the user out of the room they are in, waiting
	// for y; confirmed while it runs.
	pending   string
	confirmed bool

	running bool
	colours map[string]tcell.Color

	// Notices arrive from the client's applier as well as from commands.
	mutex   sync.Mutex
	notices []string
}

// Run takes the terminal and runs until the user stops.
func Run(c *client.Client, profile client.Profile) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	newUi(screen, c, profile).loop()
	return nil
}

// newUi builds the interface. A nil screen is one that is never drawn, for tests.
func newUi(screen tcell.Screen, c *client.Client, profile client.Profile) *Ui {
	u := &Ui{
		screen: screen, client: c, profile: profile, running: true,
		colours: map[string]tcell.Color{},
	}
	if screen != nil {
		if screen.Colors() > 0 {
			// The ANSI colours curses calls blue, cyan, yellow, red and green.
			u.colours = map[string]tcell.Color{
				"dim": tcell.ColorNavy, "me": tcell.ColorTeal, "event": tcell.ColorOlive,
				"bad": tcell.ColorMaroon, "good": tcell.ColorGreen,
			}
		}
		c.SetHandlers(u.wake, func(text string) {
			u.notice(text)
			u.wake()
		})
	}

	// A rejection decided while this user was away is why those rows are kept,
	// so it is the first thing said.
	for _, submission := range byAge(c.Submissions()) {
		if submission.State == "rejected" {
			u.notice(c.DescribeSubmission(submission))
		}
	}
	if len(c.Rooms()) == 0 {
		u.notice("No rooms yet. Tab to a person and press Enter to start one, or /open <user>.")
	}
	u.known = map[string]bool{}
	for id := range c.Rooms() {
		u.known[id] = true
	}
	return u
}

// watch notices what changed under the interface: rooms that are new, and an
// occupancy the server released because the user entered a room elsewhere.
func (u *Ui) watch() {
	me := u.client.Me()
	for id, room := range u.client.Rooms() {
		if u.known[id] {
			continue
		}
		u.known[id] = true
		if room.CreatedBy == me {
			continue
		}
		// Offered rather than entered, and held while the user is in a room.
		if u.occupancy != "" {
			u.held = append(u.held, id)
		} else {
			u.notice(invitation(room))
		}
	}
	if u.occupancy != "" && !u.client.Holds(u.occupancy) {
		u.occupancy = ""
		u.notice("You entered another room elsewhere, so you left this one")
		u.offerHeld()
	}
}

// offerHeld says which invitations arrived while the user was in a room.
func (u *Ui) offerHeld() {
	rooms := u.client.Rooms()
	for _, id := range u.held {
		if room, ok := rooms[id]; ok {
			u.notice(invitation(room))
		}
	}
	u.held = nil
}

func invitation(room client.Room) string {
	return "You were invited to " + room.Title + ": Tab to it and press Enter to join"
}

// wake has the loop redraw. Safe from any goroutine; a full queue already will.
func (u *Ui) wake() {
	_ = u.screen.PostEvent(tcell.NewEventInterrupt(nil))
}

func (u *Ui) notice(text string) {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	u.notices = append(u.notices, text)
	if len(u.notices) > noticeKeep {
		u.notices = slices.Clone(u.notices[len(u.notices)-noticeKeep:])
	}
}

func (u *Ui) recentNotices(n int) []string {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	return slices.Clone(u.notices[max(0, len(u.notices)-n):])
}

// -- the spaces the sidebar lists --------------------------------------------

// spaces is rooms then channels, each by title and then age. Ad-hoc titles may
// repeat, and age is what keeps their order from shuffling between draws.
func (u *Ui) spaces() []client.Room {
	order := func(spaces map[string]client.Room) []client.Room {
		list := slices.Collect(maps.Values(spaces))
		sort.Slice(list, func(i, j int) bool {
			a, b := strings.ToLower(list[i].Title), strings.ToLower(list[j].Title)
			if a != b {
				return a < b
			}
			if list[i].CreatedAt != list[j].CreatedAt {
				return list[i].CreatedAt < list[j].CreatedAt
			}
			return list[i].ID < list[j].ID
		})
		return list
	}
	return append(order(u.client.Rooms()), order(u.client.Channels())...)
}

func (u *Ui) ensureSelection() {
	if u.person != "" && slices.Contains(u.people(), u.person) {
		return
	}
	u.person = ""
	spaces := u.spaces()
	for _, space := range spaces {
		if space.ID == u.selected {
			return
		}
	}
	if u.occupancy != "" {
		u.notice("The room you were in has closed")
	}
	if len(spaces) > 0 {
		u.selectSpace(spaces[0].ID)
	} else {
		u.selectSpace("")
	}
}

// selectSpace highlights a space. Looking away from a room is leaving it, so any
// occupancy is given up; going into a room is enterRoom.
func (u *Ui) selectSpace(id string) {
	if id == u.selected && u.person == "" {
		return
	}
	u.release()

	u.selected, u.person = id, ""
	u.scroll, u.item, u.expanded, u.results = 0, 0, 0, nil
}

// enterRoom goes into a room, and the screen is that room until the user steps
// out. The server takes them out of any other room, on any device.
func (u *Ui) enterRoom(id string) {
	u.selectSpace(id)
	if u.occupancy != "" {
		return
	}
	occupancy, err := u.client.Enter(id)
	if err != nil {
		u.notice(err.Error())
		return
	}
	u.occupancy = occupancy
}

func (u *Ui) release() {
	if u.occupancy != "" {
		_ = u.client.Exit(u.occupancy)
		u.occupancy = ""
		u.offerHeld()
	}
}

// selectPerson looks at someone rather than a space. It leaves the room, as any
// move away does.
func (u *Ui) selectPerson(name string) {
	u.release()
	u.selected, u.person, u.scroll = "", name, 0
	u.item, u.expanded, u.results = 0, 0, nil
}

// people is every other account, in the roster's order.
func (u *Ui) people() []string {
	var names []string
	me := u.client.Me()
	for _, user := range u.client.Users() {
		if user.Username != me {
			names = append(names, user.Username)
		}
	}
	return names
}

// target is one stop on the Tab cycle: a space, or a person.
type target struct{ space, person string }

// cycle walks every space and then every other person, so a room can be raised
// without knowing a command.
func (u *Ui) cycle(step int) {
	// Inside a room the screen is that room: Tab does not leave it; /exit does.
	if u.occupancy != "" {
		return
	}
	var targets []target
	for _, space := range u.spaces() {
		targets = append(targets, target{space: space.ID})
	}
	for _, name := range u.people() {
		targets = append(targets, target{person: name})
	}
	if len(targets) == 0 {
		return
	}
	index := max(0, slices.Index(targets, target{space: u.selected, person: u.person}))
	next := targets[((index+step)%len(targets)+len(targets))%len(targets)]
	if next.person != "" {
		u.selectPerson(next.person)
	} else {
		u.selectSpace(next.space)
	}
}

// -- the loop ----------------------------------------------------------------

func (u *Ui) loop() {
	for u.running {
		u.ensureSelection()
		u.watch()
		u.draw()
		switch event := u.screen.PollEvent().(type) {
		case nil:
			u.running = false // the screen was finalised under us
		case *tcell.EventResize:
			u.screen.Sync()
		case *tcell.EventKey:
			u.key(event)
		}
	}
	u.release()
}

func (u *Ui) key(event *tcell.EventKey) {
	if u.pending != "" && event.Key() != tcell.KeyCtrlC {
		u.answer(event)
		return
	}
	switch event.Key() {
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		u.submit()
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(u.input) > 0 {
			u.input = u.input[:len(u.input)-1]
		}
	case tcell.KeyTab, tcell.KeyCtrlN:
		u.cycle(1)
	case tcell.KeyBacktab, tcell.KeyCtrlP:
		u.cycle(-1)
	case tcell.KeyPgUp:
		u.scroll += 5
	case tcell.KeyPgDn:
		u.scroll = max(0, u.scroll-5)
	case tcell.KeyUp:
		u.move(-1)
	case tcell.KeyDown:
		u.move(1)
	case tcell.KeyEscape:
		u.results, u.scroll = nil, 0
	case tcell.KeyCtrlU:
		u.input = nil
	case tcell.KeyCtrlD:
		if len(u.input) == 0 {
			u.running = false
		}
	case tcell.KeyCtrlC:
		u.running = false
	case tcell.KeyRune:
		if unicode.IsPrint(event.Rune()) {
			u.input = append(u.input, event.Rune())
		}
	}
}

func (u *Ui) submit() {
	text := strings.TrimSpace(string(u.input))
	u.input = nil
	if strings.HasPrefix(text, "/") {
		u.command(text)
		return
	}
	if u.person != "" {
		u.chat(u.person, text)
		return
	}
	space, ok := u.client.Space(u.selected)
	if text == "" {
		// Enter on nothing typed goes into a highlighted room, or opens the
		// channel item under the cursor.
		if ok && space.Kind == "room" {
			u.enterRoom(space.ID)
		} else if ok && isFeed(space) {
			u.toggle(space)
		}
		return
	}
	if u.selected == "" {
		u.notice("Nowhere to send that; open a room first")
		return
	}

	var err error
	if ok && space.Kind == "channel" {
		// A channel is read-only to its audience. A producer publishes and anyone
		// else proposes where a moderator decides; the server says which is allowed.
		// Text before the first " | " is the subject; without one the server takes
		// the first line.
		subject, body, found := strings.Cut(text, " | ")
		if !found {
			subject, body = "", text
		}
		if u.submitting(u.selected) {
			if _, err = u.client.Submit(u.selected, subject, body); err == nil {
				u.notice("Submitted: it reaches the channel if a moderator approves it")
			}
		} else {
			err = u.client.Publish(u.selected, subject, body)
		}
	} else {
		// Speaking in a room is being in it.
		u.enterRoom(u.selected)
		if u.occupancy == "" {
			return
		}
		err = u.client.Send(u.selected, text)
	}
	if err != nil {
		u.notice(err.Error())
	}
}

// submitting is whether the composer proposes rather than publishes: only in a
// channel with moderators, to someone who is neither one nor an administrator.
func (u *Ui) submitting(id string) bool {
	space, ok := u.client.Space(id)
	if !ok || space.Kind != "channel" || u.client.IsAdmin() {
		return false
	}
	return len(space.Moderators) > 0 && !slices.Contains(space.Moderators, u.client.Me())
}

// -- a channel's items -------------------------------------------------------

// isFeed is whether a space is read item by item: every channel but system.
func isFeed(space client.Room) bool {
	return space.Kind == "channel" && space.ID != systemChannel
}

// items is a channel's feed split in two, each newest first: what this user has
// not opened, and what they have. The client's own gap marker is not an item.
func (u *Ui) items(space client.Room) (pending, opened []client.Message) {
	done := u.client.Opened(space.ID)
	log := u.client.Log(space.ID)
	for index := len(log) - 1; index >= 0; index-- {
		message := log[index]
		if message.Kind == "event" {
			continue
		}
		if done[message.Seq] {
			opened = append(opened, message)
		} else {
			pending = append(pending, message)
		}
	}
	return pending, opened
}

// feedOrder is the items in the order they are drawn, which the cursor indexes.
func (u *Ui) feedOrder(space client.Room) []client.Message {
	pending, opened := u.items(space)
	return append(pending, opened...)
}

// move steps the item cursor, in a channel only.
func (u *Ui) move(step int) {
	space, ok := u.client.Space(u.selected)
	if !ok || !isFeed(space) {
		return
	}
	u.item = max(0, min(u.item+step, len(u.feedOrder(space))-1))
}

// toggle shows the body of the item under the cursor, opening it, or hides it
// again. An item opened here moves to the opened list, and the cursor follows it.
func (u *Ui) toggle(space client.Room) {
	order := u.feedOrder(space)
	if len(order) == 0 {
		return
	}
	message := order[max(0, min(u.item, len(order)-1))]
	if u.expanded == message.Seq {
		u.expanded = 0
		return
	}
	u.expanded = message.Seq
	if u.client.Opened(space.ID)[message.Seq] {
		return
	}
	if err := u.client.Open(space.ID, message.Seq); err != nil {
		u.notice(err.Error())
		return
	}
	u.item = slices.IndexFunc(u.feedOrder(space), func(m client.Message) bool { return m.Seq == message.Seq })
}

func subjectOf(message client.Message) string {
	if message.Subject != nil {
		return *message.Subject
	}
	return message.Body
}

// chat raises a room with one person and enters it, sending text as its first
// message when there is any.
func (u *Ui) chat(name, text string) {
	room, err := u.client.OpenRoom([]client.Principal{{Kind: "user", ID: name}}, "", "persisted")
	if err != nil {
		u.notice(err.Error())
		return
	}
	u.enterRoom(room.ID)
	if text == "" {
		return
	}
	if err := u.client.Send(room.ID, text); err != nil {
		u.notice(err.Error())
	}
}

// -- commands ----------------------------------------------------------------

func refusal(text string) error { return &client.ChatError{Message: text} }

// leavesRoom is the commands that show another space, and so take the user out
// of the room they are in. Inside a room they ask first.
var leavesRoom = map[string]bool{"open": true, "meet": true, "create": true, "subscribe": true}

// answer settles a pending command: y goes ahead, anything else stays.
func (u *Ui) answer(event *tcell.EventKey) {
	pending := u.pending
	u.pending = ""
	if event.Key() == tcell.KeyRune && (event.Rune() == 'y' || event.Rune() == 'Y') {
		u.confirmed = true
		u.command(pending)
		u.confirmed = false
		return
	}
	u.notice("Stayed in the room")
}

func (u *Ui) command(text string) {
	parts := strings.Fields(text[1:])
	if len(parts) == 0 {
		return
	}
	name, args := strings.ToLower(parts[0]), parts[1:]
	handler, ok := u.commands()[name]
	if !ok {
		u.notice(fmt.Sprintf("No such command: /%s -- try /help", name))
		return
	}
	if leavesRoom[name] && u.occupancy != "" && !u.confirmed {
		u.pending = text
		return
	}
	if err := handler(args); err != nil {
		u.notice(err.Error())
	}
}

func (u *Ui) commands() map[string]func([]string) error {
	return map[string]func([]string) error{
		"help": u.cmdHelp, "quit": u.cmdQuit,
		"rooms": u.cmdRooms, "people": u.cmdPeople, "groups": u.cmdGroups,
		"open": u.cmdOpen, "meet": u.cmdMeet, "create": u.cmdCreate,
		"invite": u.cmdInvite, "uninvite": u.cmdUninvite, "leave": u.cmdLeave, "exit": u.cmdExit,
		"group": u.cmdGroup, "channel": u.cmdChannel,
		"subscribe": u.cmdSubscribe, "unsubscribe": u.cmdUnsubscribe,
		"queue": u.cmdQueue, "approve": u.cmdApprove, "reject": u.cmdReject,
		"submissions": u.cmdSubmissions, "ack": u.cmdAck,
		"archive": u.cmdArchive, "archived": u.cmdArchived, "search": u.cmdSearch,
	}
}

// principal is a user, or a group when written `@name` -- the whole syntax of
// inviting a group.
func (u *Ui) principal(token string) (client.Principal, error) {
	if wanted, ok := strings.CutPrefix(token, "@"); ok {
		for _, group := range u.client.Groups() {
			if strings.EqualFold(group.Name, wanted) || group.ID == wanted {
				return client.Principal{Kind: "group", ID: group.ID}, nil
			}
		}
		return client.Principal{}, refusal("No such group: " + wanted)
	}
	return client.Principal{Kind: "user", ID: token}, nil
}

func (u *Ui) principals(tokens []string) ([]client.Principal, error) {
	found := make([]client.Principal, 0, len(tokens))
	for _, token := range tokens {
		principal, err := u.principal(token)
		if err != nil {
			return nil, err
		}
		found = append(found, principal)
	}
	return found, nil
}

// cmdHelp fills the pane rather than the notices, which show only the last
// noticeLines of a list this long. Two lines an entry, to fit a narrow pane.
func (u *Ui) cmdHelp([]string) error {
	lines := make([]string, 0, 2*len(help))
	for _, entry := range help {
		lines = append(lines, entry[0], "    "+entry[1])
	}
	u.showResults("Commands", lines)
	return nil
}

func (u *Ui) cmdQuit([]string) error {
	u.running = false
	return nil
}

func (u *Ui) cmdRooms([]string) error {
	for _, space := range u.spaces() {
		shape := space.Retention
		if space.Kind == "channel" {
			shape = space.Kind
		}
		u.notice(fmt.Sprintf("%s -- %s, %d invited [%s]",
			space.Title, shape, len(space.Audience), client.Prefix(space.ID, 8)))
	}
	return nil
}

func (u *Ui) cmdPeople([]string) error {
	for _, user := range u.client.Users() {
		state := "offline"
		if user.Online {
			state = "online "
		}
		u.notice(state + " " + user.Username)
	}
	return nil
}

func (u *Ui) cmdGroups([]string) error {
	groups := u.client.Groups()
	if len(groups) == 0 {
		u.notice("No groups yet")
	}
	for _, group := range groups {
		members := strings.Join(group.Members, ", ")
		if members == "" {
			members = "nobody"
		}
		u.notice(fmt.Sprintf("@%s -- %s", group.Name, members))
	}
	return nil
}

func (u *Ui) cmdOpen(args []string) error {
	invite, err := u.principals(args)
	if err != nil {
		return err
	}
	room, err := u.client.OpenRoom(invite, "", "persisted")
	if err != nil {
		return err
	}
	u.enterRoom(room.ID)
	return nil
}

func (u *Ui) cmdMeet(args []string) error {
	invite, err := u.principals(args)
	if err != nil {
		return err
	}
	room, err := u.client.OpenRoom(invite, "", "transient")
	if err != nil {
		return err
	}
	u.notice(room.Title + " is transient: it is discarded when everyone leaves")
	u.enterRoom(room.ID)
	return nil
}

func (u *Ui) cmdCreate(args []string) error {
	if len(args) == 0 {
		return refusal("A permanent room needs a name: /create <title>")
	}
	room, err := u.client.CreateRoom(strings.Join(args, " "), nil)
	if err != nil {
		return err
	}
	u.enterRoom(room.ID)
	return nil
}

func (u *Ui) cmdInvite(args []string) error {
	if u.selected == "" || len(args) == 0 {
		return refusal("Usage: /invite <user|@group>")
	}
	var room client.Room
	for _, token := range args {
		principal, err := u.principal(token)
		if err != nil {
			return err
		}
		if room, err = u.client.Invite(u.selected, principal); err != nil {
			return err
		}
	}
	u.notice(room.Title + ": " + strings.Join(room.Audience, ", "))
	return nil
}

func (u *Ui) cmdUninvite(args []string) error {
	if u.selected == "" || len(args) == 0 {
		return refusal("Usage: /uninvite <user|@group>")
	}
	principal, err := u.principal(args[0])
	if err != nil {
		return err
	}
	room, err := u.client.Uninvite(u.selected, principal)
	if err != nil {
		return err
	}
	u.notice(room.Title + ": " + strings.Join(room.Audience, ", "))
	return nil
}

func (u *Ui) cmdLeave([]string) error {
	if u.selected == "" {
		return nil
	}
	leaving := u.selected
	u.release()
	if err := u.client.Leave(leaving); err != nil {
		return err
	}
	u.selected = ""
	return nil
}

// cmdExit steps out of the room without giving up a place in it. The room stays
// highlighted, and the rest of the interface comes back.
func (u *Ui) cmdExit([]string) error {
	if u.occupancy == "" {
		return refusal("You are not in a room")
	}
	u.release()
	return nil
}

func (u *Ui) cmdGroup(args []string) error {
	if len(args) == 0 {
		return refusal("Usage: /group new|add|rm ...")
	}
	switch action := strings.ToLower(args[0]); {
	case action == "new" && len(args) >= 2:
		group, err := u.client.CreateGroup(args[1], args[2:])
		if err != nil {
			return err
		}
		u.notice("Created @" + group.Name)
	case (action == "add" || action == "rm") && len(args) == 3:
		group, err := u.principal("@" + args[1])
		if err != nil {
			return err
		}
		if action == "add" {
			if _, err := u.client.AssignGroup(group.ID, args[2]); err != nil {
				return err
			}
			u.notice(fmt.Sprintf("%s joined @%s, and every room it was invited to", args[2], args[1]))
		} else {
			if _, err := u.client.UnassignGroup(group.ID, args[2]); err != nil {
				return err
			}
			u.notice(fmt.Sprintf("%s left @%s, and every room it carried", args[2], args[1]))
		}
	default:
		return refusal("Usage: /group new <name> [user]... | add|rm <group> <user>")
	}
	return nil
}

func (u *Ui) cmdChannel(args []string) error {
	if len(args) >= 2 && strings.ToLower(args[0]) == "new" {
		return u.newChannel(args[1:])
	}
	actions := []string{"admit", "revoke", "appoint", "dismiss"}
	if len(args) != 3 || !slices.Contains(actions, strings.ToLower(args[0])) {
		return refusal("Usage: /channel new <title> [@group]... | admit|revoke <channel> <group>" +
			" | appoint|dismiss <channel> <user>")
	}
	action, name := strings.ToLower(args[0]), args[2]
	id, err := u.channelID(args[1])
	if err != nil {
		return err
	}

	switch action {
	case "appoint":
		channel, err := u.client.Appoint(id, name)
		if err != nil {
			return err
		}
		u.notice(fmt.Sprintf("%s moderates %s", name, channel.Title))
		return nil
	case "dismiss":
		channel, err := u.client.Dismiss(id, name)
		if err != nil {
			return err
		}
		if len(channel.Moderators) > 0 {
			u.notice(fmt.Sprintf("%s no longer moderates %s", name, channel.Title))
		} else {
			u.notice(channel.Title + " has no moderator left and takes no submissions")
		}
		return nil
	}

	group, err := u.principal("@" + name)
	if err != nil {
		return err
	}
	if action == "admit" {
		channel, err := u.client.AdmitGroup(id, group.ID)
		if err != nil {
			return err
		}
		u.notice(fmt.Sprintf("@%s may subscribe to %s", name, channel.Title))
		return nil
	}
	channel, err := u.client.RevokeGroup(id, group.ID)
	if err != nil {
		return err
	}
	if len(channel.RestrictedTo) > 0 {
		u.notice(fmt.Sprintf("@%s may no longer subscribe to %s", name, channel.Title))
	} else {
		u.notice(channel.Title + " is open to everybody again")
	}
	return nil
}

// channelID resolves the one word typed for a channel: its id, its title
// compared without case and with _ for a space, or the start of its id as
// /rooms prints it. It looks among the
// channels this client holds and those announced on system, which is how a
// channel is learned of before subscribing. What it cannot resolve goes to the
// server as typed.
func (u *Ui) channelID(typed string) (string, error) {
	known := map[string]string{} // id -> title
	for _, message := range u.client.Log(systemChannel) {
		if id, title, ok := announced(message.Body); ok {
			known[id] = title
		}
	}
	for id, channel := range u.client.Channels() {
		known[id] = channel.Title
	}
	if _, ok := known[typed]; ok || typed == "" {
		return typed, nil
	}

	var byTitle, byPrefix []string
	for id, title := range known {
		if strings.EqualFold(title, typed) || strings.EqualFold(strings.ReplaceAll(title, " ", "_"), typed) {
			byTitle = append(byTitle, id)
		}
		if strings.HasPrefix(id, typed) {
			byPrefix = append(byPrefix, id)
		}
	}
	for _, found := range [][]string{byTitle, byPrefix} {
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return "", refusal(fmt.Sprintf("'%s' names more than one channel: give more of its id", typed))
		}
	}
	return typed, nil
}

// announced reads the line system carries when a channel is founded, worded by
// the wire contract as "<user> opened the channel <title> (<id>)".
func announced(line string) (id, title string, ok bool) {
	_, rest, found := strings.Cut(line, " opened the channel ")
	open := strings.LastIndex(rest, " (")
	if !found || open < 0 || !strings.HasSuffix(rest, ")") {
		return "", "", false
	}
	return rest[open+2 : len(rest)-1], rest[:open], true
}

// newChannel takes the title first, then any groups it is restricted to, each `@name`.
func (u *Ui) newChannel(args []string) error {
	var words, groups []string
	for _, word := range args {
		if !strings.HasPrefix(word, "@") {
			words = append(words, word)
			continue
		}
		group, err := u.principal(word)
		if err != nil {
			return err
		}
		groups = append(groups, group.ID)
	}

	channel, err := u.client.CreateChannel(strings.Join(words, " "), groups)
	if err != nil {
		return err
	}
	rule := "restricted"
	if len(groups) == 0 {
		rule = "open to everybody"
	}
	u.notice(fmt.Sprintf("Opened %s (%s), %s", channel.Title, channel.ID, rule))
	return nil
}

func (u *Ui) cmdSubscribe(args []string) error {
	if len(args) != 1 {
		return refusal("Usage: /subscribe <channel>")
	}
	id, err := u.channelID(args[0])
	if err != nil {
		return err
	}
	channel, err := u.client.Subscribe(id)
	if err != nil {
		return err
	}
	u.selectSpace(channel.ID)
	return nil
}

func (u *Ui) cmdUnsubscribe([]string) error {
	if u.selected == "" {
		return nil
	}
	leaving := u.selected
	u.release()
	if err := u.client.Unsubscribe(leaving); err != nil {
		return err
	}
	u.selected = ""
	return nil
}

func (u *Ui) cmdQueue([]string) error {
	if _, ok := u.client.Channels()[u.selected]; !ok {
		return refusal("Select a channel first: /queue lists what it has to decide")
	}
	pending, err := u.client.Queue(u.selected)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		u.notice("Nothing waiting")
	}
	for _, submission := range pending {
		u.notice(u.client.DescribeSubmission(submission))
	}
	return nil
}

func (u *Ui) cmdApprove(args []string) error {
	if len(args) != 1 {
		return refusal("Usage: /approve <id>")
	}
	submission, err := resolve(args[0], u.client.Queued())
	if err != nil {
		return err
	}
	if err := u.client.Approve(submission.ID); err != nil {
		return err
	}
	u.notice(fmt.Sprintf("Approved; %s's words are in the channel now", submission.Author))
	return nil
}

func (u *Ui) cmdReject(args []string) error {
	if len(args) == 0 {
		return refusal("Usage: /reject <id> [comment]")
	}
	submission, err := resolve(args[0], u.client.Queued())
	if err != nil {
		return err
	}
	if err := u.client.Reject(submission.ID, strings.Join(args[1:], " ")); err != nil {
		return err
	}
	u.notice(fmt.Sprintf("Rejected; %s is told", submission.Author))
	return nil
}

func (u *Ui) cmdSubmissions([]string) error {
	submissions := u.client.Submissions()
	if len(submissions) == 0 {
		u.notice("Nothing you submitted is waiting")
	}
	for _, submission := range byAge(submissions) {
		u.notice(u.client.DescribeSubmission(submission))
	}
	return nil
}

func (u *Ui) cmdAck(args []string) error {
	if len(args) != 1 {
		return refusal("Usage: /ack <id>")
	}
	submission, err := resolve(args[0], u.client.Submissions())
	if err != nil {
		return err
	}
	if err := u.client.Acknowledge(submission.ID); err != nil {
		return err
	}
	u.notice("Closed")
	return nil
}

// -- archival ----------------------------------------------------------------

// cmdArchive shows or sets when the selected space's messages are archived:
// a period, "never", and "searchable" or "private" for who may search after.
func (u *Ui) cmdArchive(args []string) error {
	space, ok := u.client.Space(u.selected)
	if !ok {
		return refusal("Select a room or channel first")
	}
	if len(args) == 0 {
		u.notice(space.Title + ": " + describeArchive(space.Archive))
		return nil
	}

	var change client.ArchiveChange
	for _, arg := range args {
		switch word := strings.ToLower(arg); word {
		case "never":
			change.SetPeriod, change.Period = true, nil
		case "searchable", "private":
			searchable := word == "searchable"
			change.Searchable = &searchable
		default:
			period, err := parsePeriod(word)
			if err != nil {
				return err
			}
			change.SetPeriod, change.Period = true, &period
		}
	}
	room, err := u.client.SetArchive(space.ID, change)
	if err != nil {
		return err
	}
	u.notice(room.Title + ": " + describeArchive(room.Archive))
	return nil
}

func (u *Ui) cmdArchived(args []string) error {
	space, ok := u.client.Space(u.selected)
	if !ok {
		return refusal("Select a room or channel first")
	}
	var after int64
	if len(args) > 0 {
		parsed, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || parsed < 0 {
			return refusal("Usage: /archived [<after>]")
		}
		after = parsed
	}
	messages, more, err := u.client.ArchiveRead(space.ID, after)
	if err != nil {
		return err
	}
	lines := resultLines(messages)
	if len(lines) == 0 {
		lines = []string{"(nothing archived)"}
	}
	if more {
		lines = append(lines, fmt.Sprintf("More: /archived %d", messages[len(messages)-1].Seq))
	}
	u.showResults(fmt.Sprintf("%s: archived after %d", space.Title, after), lines)
	return nil
}

func (u *Ui) cmdSearch(args []string) error {
	space, ok := u.client.Space(u.selected)
	if !ok {
		return refusal("Select a room or channel first")
	}
	if len(args) == 0 {
		return refusal("Usage: /search <text>")
	}
	query := strings.Join(args, " ")
	messages, err := u.client.ArchiveSearch(space.ID, query)
	if err != nil {
		return err
	}
	lines := resultLines(messages)
	if len(lines) == 0 {
		lines = []string{"(no matches)"}
	}
	u.showResults(fmt.Sprintf("%s: %d archived match(es) for '%s'", space.Title, len(messages), query), lines)
	return nil
}

// showResults puts archive output in the pane, scrolled to its top.
func (u *Ui) showResults(title string, lines []string) {
	u.resultsTitle, u.results, u.scroll = title, lines, math.MaxInt32
}

// resultLines is one line per archived message: its seq, date, author, and its
// subject, or its body in a room.
func resultLines(messages []client.Message) []string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		lines = append(lines, fmt.Sprintf("[%d] %s %-*s %s", message.Seq,
			stamp(message.At, "2006-01-02 15:04"), authorWidth,
			client.Prefix(message.Author, authorWidth), subjectOf(message)))
	}
	return lines
}

// periodUnits is how many seconds each suffix a period may carry stands for.
var periodUnits = map[byte]float64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 604800}

// parsePeriod reads a period as seconds: a positive number, bare or with one
// of periodUnits.
func parsePeriod(text string) (float64, error) {
	scale := 1.0
	if n := len(text); n > 0 {
		if unit, ok := periodUnits[text[n-1]]; ok {
			scale, text = unit, text[:n-1]
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || !(value > 0) || math.IsInf(value*scale, 0) {
		return 0, refusal("Usage: /archive [<period>|never] [searchable|private], a period like 30d or 12h")
	}
	return value * scale, nil
}

// formatPeriod is seconds in the largest unit that divides them evenly.
func formatPeriod(seconds float64) string {
	for _, unit := range []struct {
		suffix string
		size   float64
	}{{"w", 604800}, {"d", 86400}, {"h", 3600}, {"m", 60}} {
		if seconds >= unit.size && math.Mod(seconds, unit.size) == 0 {
			return fmt.Sprintf("%g%s", seconds/unit.size, unit.suffix)
		}
	}
	return fmt.Sprintf("%gs", seconds)
}

func describeArchive(archive client.Archive) string {
	text := "keeps everything"
	if archive.Period != nil {
		text = "archives after " + formatPeriod(*archive.Period)
	}
	if archive.Searchable {
		return text + "; the archive is searchable"
	}
	return text + "; the archive is the administrator's"
}

// resolve is the one submission whose id starts with prefix, as the listings print it.
func resolve(prefix string, pool map[string]client.Submission) (client.Submission, error) {
	var matches []client.Submission
	for id, submission := range pool {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, submission)
		}
	}
	if len(matches) != 1 {
		return client.Submission{}, refusal(fmt.Sprintf(
			"No one submission matches '%s': /queue or /submissions lists them", prefix))
	}
	return matches[0], nil
}

func byAge(pool map[string]client.Submission) []client.Submission {
	list := slices.Collect(maps.Values(pool))
	sort.Slice(list, func(i, j int) bool {
		if list[i].At != list[j].At {
			return list[i].At < list[j].At
		}
		return list[i].ID < list[j].ID
	})
	return list
}

// -- drawing -----------------------------------------------------------------

var plain = tcell.StyleDefault

// tint applies a named colour, when the terminal has colours at all.
func (u *Ui) tint(style tcell.Style, name string) tcell.Style {
	if colour, ok := u.colours[name]; ok {
		return style.Foreground(colour)
	}
	return style
}

func (u *Ui) draw() {
	u.screen.Clear()
	width, height := u.screen.Size()
	if height < 6 || width < 40 {
		u.put(0, 0, "Terminal too small", width, plain, false)
		u.screen.HideCursor()
		u.screen.Show()
		return
	}

	// Inside a room the screen is that room: no sidebar, the pane full width.
	left := sidebarWidth + 2
	if u.occupancy != "" {
		left = 1
	} else {
		u.drawSidebar(height)
	}
	// The pane first: it marks what is on screen read, and the status counts it.
	u.drawPane(height, width, left)
	u.drawStatus(width)
	u.drawComposer(height, width)
	u.screen.Show()
}

// put draws text into a field, optionally clearing the rest of it.
//
// A control character is drawn as '?': a body is another user's text, and an
// escape sequence written raw would be obeyed by the terminal.
func (u *Ui) put(y, x int, text string, width int, style tcell.Style, pad bool) {
	if y < 0 || x < 0 || width <= 0 {
		return
	}
	runes := []rune(text)
	if len(runes) > width {
		runes = runes[:width]
	}
	for pad && len(runes) < width {
		runes = append(runes, ' ')
	}
	for offset, r := range runes {
		if unicode.IsControl(r) {
			r = '?'
		}
		u.screen.SetContent(x+offset, y, r, nil, style)
	}
}

func (u *Ui) drawStatus(width int) {
	who := u.profile.Username
	if who == "" {
		who = "?"
	}
	if u.client.IsAdmin() {
		who += " (admin)"
	}
	if space, ok := u.client.Space(u.selected); ok && u.occupancy != "" {
		who += "  in " + space.Title
	}
	// Two counts, never mixed: rooms not yet entered, and what is unread in rooms
	// that have been. A channel's items are counted only inside the channel.
	who += count(int64(u.client.OpenInvitations()), "open invitation")
	who += count(u.client.UnreadMessages(), "unread message")
	state, colour := "disconnected", "bad"
	if u.client.Connected() {
		state, colour = "connected", "good"
	}

	reverse := plain.Reverse(true)
	u.put(0, 0, ljust(" minos  "+who, width), width, reverse, false)
	u.put(0, max(0, width-len(state)-2), state, len(state)+1, u.tint(reverse, colour), false)
}

// count is "  n things" for the status line, and nothing at zero.
func count(n int64, thing string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "  1 " + thing
	}
	return fmt.Sprintf("  %d %ss", n, thing)
}

func (u *Ui) drawSidebar(height int) {
	bottom := height - 3
	row := 1
	spaces := u.spaces()
	ambiguous := ambiguousTitles(spaces)
	heading := u.tint(plain.Bold(true), "dim")
	dim := u.tint(plain, "dim")

	var rooms, channels []client.Room
	for _, space := range spaces {
		if space.Kind == "channel" {
			channels = append(channels, space)
		} else {
			rooms = append(rooms, space)
		}
	}

	for _, section := range []struct {
		title  string
		spaces []client.Room
	}{{"ROOMS", rooms}, {"CHANNELS", channels}} {
		if row >= bottom {
			break
		}
		u.put(row, 1, section.title, sidebarWidth-2, heading, true)
		row++
		if len(section.spaces) == 0 {
			u.put(row, 2, "(none)", sidebarWidth-3, dim, true)
			row++
		}
		for _, space := range section.spaces {
			if row >= bottom {
				break
			}
			row = u.drawSpace(row, space, ambiguous)
		}
		row++
	}

	if row < bottom {
		u.put(row, 1, "PEOPLE", sidebarWidth-2, heading, true)
		row++
		me := u.client.Me()
		for _, user := range u.client.Users() {
			if row >= bottom || user.Username == me {
				continue
			}
			mark, style := " ", dim
			if user.Online {
				mark, style = "*", u.tint(plain, "good")
			}
			marker := " "
			if user.Username == u.person {
				marker, style = ">", style.Bold(true)
			}
			u.put(row, 1, marker+mark+" "+user.Username, sidebarWidth-2, style, true)
			row++
		}
	}

	for line := row; line < bottom; line++ {
		u.put(line, 1, "", sidebarWidth-2, plain, true)
	}
	for line := 1; line < height-3; line++ {
		u.put(line, sidebarWidth, "|", 1, dim, false)
	}
}

// ambiguousTitles is the titles held by more than one space. Only ad-hoc rooms
// collide, and when each began is how anybody would tell them apart.
func ambiguousTitles(spaces []client.Room) map[string]bool {
	seen, twice := map[string]bool{}, map[string]bool{}
	for _, space := range spaces {
		if seen[space.Title] {
			twice[space.Title] = true
		}
		seen[space.Title] = true
	}
	return twice
}

func (u *Ui) drawSpace(row int, space client.Room, ambiguous map[string]bool) int {
	selected := space.ID == u.selected
	unread := u.client.Unread(space.ID)

	marker := " "
	if selected {
		marker = ">"
	}
	label := space.Title
	if ambiguous[space.Title] {
		label += stamp(space.CreatedAt, " 15:04:05")
	}
	if space.Kind == "room" && space.Retention == "transient" {
		label += " ~"
	}

	style := plain
	if selected {
		style = style.Bold(true)
	}
	text := marker + " " + label
	switch {
	case space.Kind == "room" && u.client.OpenInvitation(space.ID):
		// Not yet entered, so its messages are not unread: they have never been seen.
		style = u.tint(style, "me")
		text += " (invited)"
	case unread > 0 && !selected:
		style = u.tint(style, "me")
		text = fmt.Sprintf("%s (%d)", text, unread)
	}
	u.put(row, 1, text, sidebarWidth-2, style, true)
	return row + 1
}

type line struct {
	text  string
	style tcell.Style
}

func (u *Ui) drawPane(height, width, left int) {
	pane := width - left
	bottom := height - 3

	if u.results != nil {
		u.drawResults(left, pane, bottom)
		return
	}
	if u.person != "" {
		u.drawPerson(left, pane, bottom)
		return
	}
	space, ok := u.client.Space(u.selected)
	if u.selected == "" || !ok {
		u.put(1, left, "No room selected. /open <user> to raise one,", pane, plain, false)
		u.put(2, left, "or /help for what else there is.", pane, plain, false)
		return
	}

	header := space.Title
	if space.Kind == "channel" {
		header += "  (channel: read-only)"
	} else if space.Retention == "transient" {
		header += "  (transient: discarded when everyone leaves)"
	}
	u.put(1, left, header, pane, plain.Bold(true), true)
	who := fmt.Sprintf("%d invited", len(space.Audience))
	if len(space.Occupants) > 0 {
		who += ", here now: " + strings.Join(space.Occupants, ", ")
	}
	if space.Kind == "room" && u.occupancy == "" {
		who += "  (Enter to join)"
	}
	u.put(2, left, who, pane, u.tint(plain, "dim"), true)

	if isFeed(space) {
		u.drawFeed(space, left, pane, bottom)
		return
	}

	lines := u.paneLines(space, pane)
	visible := max(0, bottom-4)
	u.scroll = max(0, min(u.scroll, max(0, len(lines)-visible)))
	end := len(lines) - u.scroll
	for offset, line := range lines[max(0, end-visible):end] {
		u.put(4+offset, left, line.text, pane, line.style, true)
	}

	// Only a room has a read cursor, and only one the user is in counts as seen: a
	// highlighted room is a preview.
	if u.scroll == 0 && space.Kind == "room" && u.occupancy != "" {
		u.markRead(space)
	}
}

// drawFeed draws a channel as its items: the pending ones, then the opened ones,
// each a subject line, with the body shown under the one that is expanded.
func (u *Ui) drawFeed(space client.Room, left, pane, bottom int) {
	pending, opened := u.items(space)
	u.item = max(0, min(u.item, len(pending)+len(opened)-1))

	var lines []line
	focus, focusEnd, index := 0, 0, 0
	dim := u.tint(plain, "dim")
	section := func(title string, messages []client.Message, style tcell.Style) {
		lines = append(lines, line{title, plain.Bold(true)})
		if len(messages) == 0 {
			lines = append(lines, line{"  (none)", dim})
		}
		for _, message := range messages {
			marker, itemStyle := "  ", style
			if index == u.item {
				marker, itemStyle, focus = "> ", itemStyle.Reverse(true), len(lines)
			}
			lines = append(lines, line{fmt.Sprintf("%s%s %-*s %s", marker, stamp(message.At, "15:04"),
				authorWidth, client.Prefix(message.Author, authorWidth), subjectOf(message)), itemStyle})
			if message.Seq == u.expanded {
				body := wrap(message.Body, max(10, pane-4))
				if strings.TrimSpace(message.Body) == subjectOf(message) {
					body = nil // the subject was the whole of it
				}
				for _, part := range body {
					lines = append(lines, line{"    " + part, plain})
				}
				if len(body) == 0 {
					lines = append(lines, line{"    (nothing more)", dim})
				}
			}
			if index == u.item {
				focusEnd = len(lines) - 1
			}
			index++
		}
		lines = append(lines, line{"", plain})
	}
	section(fmt.Sprintf("Pending (%d)", len(pending)), pending, u.tint(plain.Bold(true), "me"))
	section("Opened", opened, dim)
	lines = append(lines, u.sessionLines(pane)...)

	// Scrolled so the item under the cursor, body included, is on screen.
	visible := max(0, bottom-4)
	start := max(0, min(focusEnd-visible+1, focus))
	for offset, line := range lines[start:min(len(lines), start+visible)] {
		u.put(4+offset, left, line.text, pane, line.style, true)
	}
}

// paneLines is every message in this space, wrapped, and then the notice log.
func (u *Ui) paneLines(space client.Room, pane int) []line {
	var lines []line
	me := u.client.Me()
	for _, message := range u.client.Log(space.ID) {
		author := message.Author
		if author == "" {
			author = "?"
		}
		style := plain
		if message.Kind == "event" {
			style = u.tint(plain, "event")
		} else if author == me {
			style = u.tint(plain, "me")
		}

		prefix := fmt.Sprintf("%s %-*s ", stamp(message.At, "15:04"), authorWidth, client.Prefix(author, authorWidth))
		indent := len([]rune(prefix))
		wrapped := wrap(message.Body, max(10, pane-indent))
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		lines = append(lines, line{prefix + wrapped[0], style})
		for _, extra := range wrapped[1:] {
			lines = append(lines, line{strings.Repeat(" ", indent) + extra, style})
		}
	}

	return append(lines, u.sessionLines(pane)...)
}

// sessionLines is the recent command output. It belongs to the session rather
// than to a room, so it sits below a rule where it cannot be taken for speech.
func (u *Ui) sessionLines(pane int) []line {
	recent := u.recentNotices(noticeLines)
	if len(recent) == 0 {
		return nil
	}
	dim := u.tint(plain, "dim")
	lines := []line{{strings.Repeat("-", max(4, min(pane, 40))), dim}}
	for _, text := range recent {
		wrapped := wrap(text, max(10, pane-2))
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, part := range wrapped {
			lines = append(lines, line{"  " + part, dim})
		}
	}
	return lines
}

// drawResults shows archive output in place of the conversation. PgUp and PgDn
// scroll it as they scroll a room.
func (u *Ui) drawResults(left, pane, bottom int) {
	u.put(1, left, u.resultsTitle, pane, plain.Bold(true), true)
	u.put(2, left, "Esc: back to the conversation", pane, u.tint(plain, "dim"), true)

	lines := make([]line, 0, len(u.results))
	for _, text := range u.results {
		lines = append(lines, line{text, plain})
	}
	lines = append(lines, u.sessionLines(pane)...)
	visible := max(0, bottom-4)
	u.scroll = max(0, min(u.scroll, max(0, len(lines)-visible)))
	end := len(lines) - u.scroll
	for offset, line := range lines[max(0, end-visible):end] {
		u.put(4+offset, left, line.text, pane, line.style, true)
	}
}

// drawPerson is the pane for someone in PEOPLE. It lists the rooms already shared
// with them, because Enter raises another rather than reusing one.
func (u *Ui) drawPerson(left, pane, bottom int) {
	state := "offline"
	for _, user := range u.client.Users() {
		if user.Username == u.person && user.Online {
			state = "online"
		}
	}
	u.put(1, left, u.person, pane, plain.Bold(true), true)
	u.put(2, left, state, pane, u.tint(plain, "dim"), true)

	texts := []string{"Enter raises a room with " + u.person + "; anything typed first is its first message."}
	var shared []string
	for _, space := range u.spaces() {
		if space.Kind == "room" && slices.Contains(space.Audience, u.person) {
			shared = append(shared, space.Title)
		}
	}
	if len(shared) > 0 {
		texts = append(texts, "Rooms you already share: "+strings.Join(shared, "; "))
	}
	var lines []line
	for _, text := range texts {
		for _, part := range wrap(text, max(10, pane)) {
			lines = append(lines, line{part, plain})
		}
	}
	lines = append(lines, u.sessionLines(pane)...)
	visible := max(0, bottom-4)
	for offset, line := range lines[max(0, len(lines)-visible):] {
		u.put(4+offset, left, line.text, pane, line.style, true)
	}
}

// markRead moves the read cursor when the newest message is on screen. Seen, as
// opposed to received, and kept by the server because it is the same from every device.
func (u *Ui) markRead(space client.Room) {
	if space.LastSeq > u.client.ReadCursor(space.ID) {
		_ = u.client.MarkRead(space.ID, space.LastSeq)
	}
}

func (u *Ui) drawComposer(height, width int) {
	space, ok := u.client.Space(u.selected)
	readOnly := ok && space.Kind == "channel"

	u.put(height-3, 0, strings.Repeat("-", width), width, u.tint(plain, "dim"), false)
	prompt := "> "
	hint := " /help  Tab: next  PgUp/PgDn: scroll  ^C: quit"
	switch {
	case u.person != "":
		prompt = "  (to " + u.person + ") "
		hint = " Enter: open a room with " + u.person + "  Tab: next  ^C: quit"
	case u.submitting(u.selected):
		prompt = "  (submit) "
	case readOnly:
		prompt = "  (channel) "
	case ok && u.occupancy != "":
		hint = " /exit: step out  /invite <who>  /leave  /help  PgUp/PgDn: scroll  ^C: quit"
	case ok:
		hint = " Enter: join  Tab: next  /help  ^C: quit"
	}
	if ok && isFeed(space) && u.person == "" {
		hint = " Up/Down: item  Enter: open or close  subject | body  Tab: next  ^C: quit"
	}
	if u.results != nil {
		hint = " Esc: back  PgUp/PgDn: scroll  ^C: quit"
	}
	if u.pending != "" {
		title := "this room"
		if ok {
			title = space.Title
		}
		prompt = fmt.Sprintf("  %s takes you out of %s. y to go, any other key to stay ", u.pending, title)
		hint = " y: go  any other key: stay  ^C: quit"
	}
	text := prompt + string(u.input)
	u.put(height-2, 0, text, width-1, plain, true)

	u.put(height-1, 0, ljust(hint, width), width, u.tint(plain.Reverse(true), "dim"), false)
	u.screen.ShowCursor(min(len([]rune(text)), width-1), height-2)
}

// -- text --------------------------------------------------------------------

func stamp(at float64, layout string) string {
	return time.Unix(0, int64(at*float64(time.Second))).Local().Format(layout)
}

func ljust(text string, width int) string {
	if gap := width - len([]rune(text)); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// wrap fills lines of at most width characters the way Python's textwrap does:
// whitespace collapses, and a word longer than a line fills what is left of it.
func wrap(text string, width int) []string {
	var lines []string
	var current []rune
	for _, word := range strings.Fields(text) {
		rest := []rune(word)
		for len(rest) > 0 {
			gap := 0
			if len(current) > 0 {
				gap = 1
			}
			if len(current)+gap+len(rest) <= width {
				if gap == 1 {
					current = append(current, ' ')
				}
				current = append(current, rest...)
				break
			}
			if len(rest) > width {
				if room := width - len(current) - gap; room > 0 {
					if gap == 1 {
						current = append(current, ' ')
					}
					current = append(current, rest[:room]...)
					rest = rest[room:]
				}
			}
			lines = append(lines, string(current))
			current = nil
		}
	}
	if len(current) > 0 {
		lines = append(lines, string(current))
	}
	return lines
}
