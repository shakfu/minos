// Package tui is the terminal interface.
//
// A room is a place, and the interface makes that literal: the room you have
// selected is the room you occupy, and switching away leaves it. For a transient
// room that is what keeps it alive, and closing the program is leaving.
//
// Everything the retired desktop expressed by dragging is a command here. The
// sidebar lists what you can enter, the pane shows one conversation, the
// composer takes text or a command, and the status line says whether the socket
// is up -- in a terminal there is nowhere else to notice that it is not.
package tui

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
)

const (
	sidebarWidth = 24
	authorWidth  = 10

	// Lines of command output kept under the conversation, and in memory.
	noticeLines = 6
	noticeKeep  = 200
)

var help = [][2]string{
	{"/rooms  /people  /groups", "list what there is"},
	{"/open <who>...", "raise an ad-hoc room, kept"},
	{"/meet <who>...", "raise an ad-hoc room, discarded when everyone leaves"},
	{"/create <title>", "found a permanent room (admin)"},
	{"/invite <who>", "admit a user, or @group"},
	{"/uninvite <who>", "withdraw a grant"},
	{"/leave", "give up your place in this room"},
	{"/group new <name> [user]...", "create a group (admin)"},
	{"/group add|rm <group> <user>", "assign or unassign (admin)"},
	{"/subscribe <id>  /unsubscribe", "a channel's audience is your own choice"},
	{"/channel new <title> [@group]...", "found a channel (admin)"},
	{"/channel admit|revoke <id> <group>", "restrict a channel to groups (admin)"},
	{"/channel appoint|dismiss <id> <user>", "who moderates a channel (admin)"},
	{"/queue", "what this channel's moderators have to decide"},
	{"/approve <id>  /reject <id> [why]", "decide a submission (moderator)"},
	{"/submissions  /ack <id>", "what you submitted, and closing a rejection"},
	{"/quit", "leave every room and stop"},
	{"Tab / S-Tab", "next or previous space"},
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
	occupancy string
	running   bool
	colours   map[string]tcell.Color

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
	return u
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
	spaces := u.spaces()
	for _, space := range spaces {
		if space.ID == u.selected {
			return
		}
	}
	if len(spaces) > 0 {
		u.selectSpace(spaces[0].ID)
	} else {
		u.selectSpace("")
	}
}

// selectSpace moves between spaces, taking and giving up occupancy as it goes:
// a transient room's life is measured by it, so it follows the selection.
func (u *Ui) selectSpace(id string) {
	if id == u.selected {
		return
	}
	u.release()

	u.selected = id
	u.scroll = 0
	if space, ok := u.client.Space(id); ok && space.Kind == "room" {
		occupancy, err := u.client.Enter(id)
		if err != nil {
			u.notice(err.Error())
		} else {
			u.occupancy = occupancy
		}
	}
}

func (u *Ui) release() {
	if u.occupancy != "" {
		_ = u.client.Exit(u.occupancy)
		u.occupancy = ""
	}
}

func (u *Ui) cycle(step int) {
	spaces := u.spaces()
	if len(spaces) == 0 {
		return
	}
	index := slices.IndexFunc(spaces, func(space client.Room) bool { return space.ID == u.selected })
	index = max(0, index)
	u.selectSpace(spaces[((index+step)%len(spaces)+len(spaces))%len(spaces)].ID)
}

// -- the loop ----------------------------------------------------------------

func (u *Ui) loop() {
	for u.running {
		u.ensureSelection()
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
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		u.command(text)
		return
	}
	if u.selected == "" {
		u.notice("Nowhere to send that; open a room first")
		return
	}

	var err error
	if space, ok := u.client.Space(u.selected); ok && space.Kind == "channel" {
		// A channel is read-only to its audience. A producer publishes and anyone
		// else proposes where a moderator decides; the server says which is allowed.
		if u.submitting(u.selected) {
			if _, err = u.client.Submit(u.selected, text); err == nil {
				u.notice("Submitted: it reaches the channel if a moderator approves it")
			}
		} else {
			err = u.client.Publish(u.selected, text)
		}
	} else {
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

// -- commands ----------------------------------------------------------------

func refusal(text string) error { return &client.ChatError{Message: text} }

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
	if err := handler(args); err != nil {
		u.notice(err.Error())
	}
}

func (u *Ui) commands() map[string]func([]string) error {
	return map[string]func([]string) error{
		"help": u.cmdHelp, "quit": u.cmdQuit,
		"rooms": u.cmdRooms, "people": u.cmdPeople, "groups": u.cmdGroups,
		"open": u.cmdOpen, "meet": u.cmdMeet, "create": u.cmdCreate,
		"invite": u.cmdInvite, "uninvite": u.cmdUninvite, "leave": u.cmdLeave,
		"group": u.cmdGroup, "channel": u.cmdChannel,
		"subscribe": u.cmdSubscribe, "unsubscribe": u.cmdUnsubscribe,
		"queue": u.cmdQueue, "approve": u.cmdApprove, "reject": u.cmdReject,
		"submissions": u.cmdSubmissions, "ack": u.cmdAck,
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

func (u *Ui) cmdHelp([]string) error {
	for _, line := range help {
		u.notice(fmt.Sprintf("%-30s %s", line[0], line[1]))
	}
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
	u.selectSpace(room.ID)
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
	u.selectSpace(room.ID)
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
	u.selectSpace(room.ID)
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
		return refusal("Usage: /channel new <title> [@group]... | admit|revoke <id> <group>" +
			" | appoint|dismiss <id> <user>")
	}
	action, id, name := strings.ToLower(args[0]), args[1], args[2]

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
	if len(args) == 0 {
		return refusal("Usage: /subscribe <channel-id>")
	}
	channel, err := u.client.Subscribe(args[0])
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

	u.drawStatus(width)
	u.drawSidebar(height)
	u.drawPane(height, width)
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
	state, colour := "disconnected", "bad"
	if u.client.Connected() {
		state, colour = "connected", "good"
	}

	reverse := plain.Reverse(true)
	u.put(0, 0, ljust(" minos  "+who, width), width, reverse, false)
	u.put(0, max(0, width-len(state)-2), state, len(state)+1, u.tint(reverse, colour), false)
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
			u.put(row, 2, mark+" "+user.Username, sidebarWidth-3, style, true)
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
	if unread > 0 && !selected {
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

func (u *Ui) drawPane(height, width int) {
	left := sidebarWidth + 2
	pane := width - left
	bottom := height - 3

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
	u.put(2, left, who, pane, u.tint(plain, "dim"), true)

	lines := u.paneLines(space, pane)
	visible := max(0, bottom-4)
	u.scroll = max(0, min(u.scroll, max(0, len(lines)-visible)))
	end := len(lines) - u.scroll
	for offset, line := range lines[max(0, end-visible):end] {
		u.put(4+offset, left, line.text, pane, line.style, true)
	}

	if u.scroll == 0 {
		u.markRead(space)
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

	// Command output belongs to the session rather than to this room, so it sits
	// below a rule where it cannot be taken for something said here.
	if recent := u.recentNotices(noticeLines); len(recent) > 0 {
		dim := u.tint(plain, "dim")
		lines = append(lines, line{strings.Repeat("-", max(4, min(pane, 40))), dim})
		for _, text := range recent {
			wrapped := wrap(text, max(10, pane-2))
			if len(wrapped) == 0 {
				wrapped = []string{""}
			}
			for _, part := range wrapped {
				lines = append(lines, line{"  " + part, dim})
			}
		}
	}
	return lines
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
	if u.submitting(u.selected) {
		prompt = "  (submit) "
	} else if readOnly {
		prompt = "  (channel) "
	}
	text := prompt + string(u.input)
	u.put(height-2, 0, text, width-1, plain, true)

	hint := " /help  Tab: next  PgUp/PgDn: scroll  ^C: quit"
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
