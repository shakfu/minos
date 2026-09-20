// Package tui is the terminal interface.
//
// A room is a place, and the interface makes that literal: the room you enter
// is the room you occupy, and while you are in it the screen is that room.
// Looking away leaves it. For a transient room that is what keeps it alive, and
// closing the program is leaving.
//
// Everything the retired desktop expressed by dragging is a command here. A
// tab bar lists what there is, each tab a table: projects, then the rooms of
// one project, then the room itself. Tab moves between tabs, Enter goes one
// level in, Esc one level out. The composer takes text or a command, and the
// status line says whether the socket is up -- in a terminal there is nowhere
// else to notice that it is not.
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
	"github.com/rivo/uniseg"

	"minos/internal/client"
)

// systemChannel is the machine channel's id, fixed by the wire contract. It is
// a log, with nothing pending and nothing to open.
const systemChannel = "system"

const (
	authorWidth = 10

	// Lines of command output kept under the conversation, and in memory.
	noticeLines = 6
	noticeKeep  = 200
)

// tab is one entry in the tab bar, and the list it opens on.
type tab int

const (
	tabOverview tab = iota
	tabProjects
	tabRooms
	tabPeople
)

// tabs are the tab bar's entries, in the order it shows them and Tab moves
// through them.
var tabs = []tab{tabOverview, tabProjects, tabRooms, tabPeople}

func (t tab) String() string {
	switch t {
	case tabProjects:
		return "PROJECTS"
	case tabRooms:
		return "ROOMS"
	case tabPeople:
		return "PEOPLE"
	}
	return "OVERVIEW"
}

// activeWindow is how far back what was said counts towards a place being
// busy, and overviewTop how many projects the overview ranks.
const (
	activeWindow = 7 * 24 * time.Hour
	overviewTop  = 5
)

// view is what the body shows: one of the tabs' lists, one project's page, or
// a space.
type view int

const (
	viewOverview view = iota
	viewProjects
	viewProject
	viewRooms
	viewPeople
	viewSpace
)

// unfiled stands for the rooms under no project, in the projects list.
const unfiled = "\x00none"

var help = [][2]string{
	{"/help", "this list"},
	{"/rooms  /people  /groups  /projects", "list what there is"},
	{"/open <who>...", "raise an ad-hoc room, kept"},
	{"/meet <who>...", "raise an ad-hoc room, discarded when everyone leaves"},
	{"/create [project/]<title>", "found a permanent room (admin)"},
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
	{"project/<title>", "naming a place outside the project you are looking at"},
	{"/queue", "what this channel's moderators have to decide"},
	{"/approve <id>  /reject <id> [why]", "decide a submission (moderator)"},
	{"/submissions  /ack <id>", "what you submitted, and closing a rejection"},
	{"/archive [<period>|never] [searchable|private]", "when this space is archived (admin)"},
	{"/archived [<after>]", "read this space's archive (admin)"},
	{"/search <text>", "search this space's archive, where allowed"},
	{"Esc", "close archive results"},
	{"/quit", "leave every room and stop"},
	{"/project new <name> [#tag]...", "found a project (admin)"},
	{"/project tag|untag <name> <tag>", "classify a project (admin)"},
	{"/project file <room> [name] [task]", "file a room under a project, or under none (admin)"},
	{"/project rm <name>", "dissolve a project; its rooms survive (admin)"},
	{"/close  /reopen [room]", "say whether the work in a place is done"},
	{"Tab / S-Tab", "next or previous tab, outside a room"},
	{"Up / Down", "move through the list, or a channel's items"},
	{"a", "in a list of places, show the closed ones too"},
	{"Enter on a project", "open its page: what it holds, and its places"},
	{"Enter on a room", "go in: the screen is that room until you step out"},
	{"Enter on a person", "raise a room with them"},
	{"Esc", "back one level"},
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

	// The tab bar's current entry, and what the body shows. A list drills into
	// the next one; the last is the space itself.
	tab  tab
	view view

	// The project the rooms list is limited to, when it was reached from the
	// projects list. scoped tells a scope of unfiled from no scope at all.
	scope  string
	scoped bool

	// The row under the cursor in whichever list is shown.
	cursor int

	// Whether a list of places shows the closed ones too. Off on every move to
	// a tab or a project, because what is active is what a list is for.
	showClosed bool

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

	// now is the clock the activity window is measured against; tests replace it.
	now func() time.Time

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
		colours: map[string]tcell.Color{}, now: time.Now,
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
		u.notice("No rooms yet. Tab to PEOPLE and press Enter on someone, or /open <user>.")
	}
	// Where the work is, which is what a session opens to want to know.
	u.showTab(tabOverview)
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

// ensureSelection keeps the cursor and the selection on something that still
// exists: a room can close, and a project can be dissolved, under either.
func (u *Ui) ensureSelection() {
	if u.view == viewSpace || u.view == viewRooms || u.view == viewProject {
		if _, ok := u.client.Space(u.selected); u.selected != "" && !ok {
			// The room went away under us. Back to the list it was listed in.
			if u.occupancy != "" {
				u.notice("The room you were in has closed")
				u.occupancy = ""
			}
			u.selected = ""
			u.view = viewRooms
		}
	}
	// A dissolved project leaves its page with nothing to show.
	if u.view == viewProject && u.scope != unfiled && u.project(u.scope) == nil {
		u.scoped, u.scope, u.cursor = false, "", 0
		u.view = viewProjects
	}
	u.cursor = max(0, min(u.cursor, u.rowCount()-1))
	if u.view == viewRooms || u.view == viewProject {
		u.followCursor()
	}
}

// project is one project by id, or nil.
func (u *Ui) project(id string) *client.Project {
	for _, project := range u.client.Projects() {
		if project.ID == id {
			return &project
		}
	}
	return nil
}

// rowCount is how many rows the list on screen has.
func (u *Ui) rowCount() int {
	switch u.view {
	case viewOverview:
		return len(u.overviewRows())
	case viewProjects:
		return len(u.projectRows())
	case viewProject, viewRooms:
		return len(u.roomRows())
	case viewPeople:
		return len(u.people())
	}
	return 0
}

// followCursor makes the room under the cursor the selected one, so the status
// line, the composer and the room commands all speak about what is highlighted.
func (u *Ui) followCursor() {
	rows := u.roomRows()
	if len(rows) == 0 {
		u.selectSpace("")
		return
	}
	u.selectSpace(rows[min(u.cursor, len(rows)-1)].ID)
}

// showTab opens a tab's list, leaving any room and any drill-down behind.
func (u *Ui) showTab(next tab) {
	u.tab, u.cursor = next, 0
	u.scope, u.scoped = "", false
	u.results, u.resultsTitle = nil, ""
	u.showClosed = false
	switch next {
	case tabProjects:
		u.view = viewProjects
		u.selectSpace("")
		u.person = ""
	case tabRooms:
		u.view = viewRooms
		u.person = ""
		u.followCursor()
	case tabPeople:
		u.view = viewPeople
		u.selectSpace("")
		if names := u.people(); len(names) > 0 {
			u.selectPerson(names[0])
		}
	default:
		u.view = viewOverview
		u.selectSpace("")
		u.person = ""
	}
}

// enterRow goes one level in from the list on screen.
func (u *Ui) enterRow() {
	switch u.view {
	case viewOverview:
		rows := u.overviewRows()
		if len(rows) == 0 {
			return
		}
		u.openProject(rows[min(u.cursor, len(rows)-1)].id)
	case viewProjects:
		rows := u.projectRows()
		if len(rows) == 0 {
			return
		}
		u.openProject(rows[min(u.cursor, len(rows)-1)].id)
	case viewProject, viewRooms:
		rows := u.roomRows()
		if len(rows) == 0 {
			return
		}
		u.openSpace(rows[min(u.cursor, len(rows)-1)])
	case viewPeople:
		if u.person != "" {
			u.chat(u.person, "")
		}
	case viewSpace:
		if space, ok := u.client.Space(u.selected); ok && isFeed(space) {
			u.toggle(space)
		}
	}
}

// openProject opens one project's page: what it holds, and its places. The tab
// bar stays where it was, because this is a level under a tab, not another tab.
func (u *Ui) openProject(id string) {
	u.scope, u.scoped, u.cursor = id, true, 0
	u.showClosed = false
	u.view = viewProject
	u.followCursor()
}

// openSpace shows one room or channel, and the screen is that space. A room is
// entered, which is what being in it means; a channel has no occupancy.
func (u *Ui) openSpace(space client.Room) {
	u.selectSpace(space.ID)
	u.view = viewSpace
	u.scroll, u.item, u.expanded = 0, 0, 0
	if space.Kind == "room" {
		u.enterRoom(space.ID)
	}
}

// exited is what /exit and a released occupancy leave behind: out of the room,
// back on the list it was listed in.
func (u *Ui) exited() {
	if u.view == viewSpace {
		u.view = viewRooms
		u.selectSpace("")
		u.followCursor()
	}
}

// back steps one level out: a space to the list it was in, a project's rooms to
// the projects, and a tab's own list no further.
func (u *Ui) back() {
	switch {
	case u.results != nil:
		u.results, u.resultsTitle, u.scroll = nil, "", 0
	case u.view == viewSpace:
		u.release()
		u.exited()
	case u.view == viewProject:
		opened := u.scope
		u.scope, u.scoped, u.showClosed = "", false, false
		u.selectSpace("")
		if u.tab == tabOverview {
			u.view = viewOverview
			rows := u.overviewRows()
			u.cursor = max(0, slices.IndexFunc(rows, func(r projectRow) bool { return r.id == opened }))
			return
		}
		u.view = viewProjects
		rows := u.projectRows()
		u.cursor = max(0, slices.IndexFunc(rows, func(r projectRow) bool { return r.id == opened }))
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
	// Being in a room is the screen being that room, however the user got
	// there: Enter on a row, /open, or Enter on a person.
	u.view, u.scroll = viewSpace, 0
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

// cycle moves to the next tab, wrapping. A drill-down is left behind: a tab is
// the top level, and Tab always lands on one.
func (u *Ui) cycle(step int) {
	// Inside a room the screen is that room: Tab does not leave it; /exit does.
	if u.occupancy != "" {
		return
	}
	index := max(0, slices.Index(tabs, u.tab))
	u.showTab(tabs[((index+step)%len(tabs)+len(tabs))%len(tabs)])
}

// projectRow is one line of the projects list. A project holds no messages, so
// what it is worth showing is what its places hold.
//
// active counts the places still open, which is a fact somebody set; said
// counts what was said in them lately, which is how busy they are now. The two
// answer different questions and the overview needs both.
type projectRow struct {
	id      string
	name    string
	tags    []string
	rooms   int
	feeds   int
	active  int
	closed  int
	said    int
	busiest string
	unread  int64
	invited int
	last    float64
}

// projectRows is every project, and last a row for the rooms under none, which
// exists only when there are any. A project with no rooms still has a row: a
// project survives being empty.
func (u *Ui) projectRows() []projectRow {
	rows := []projectRow{}
	at := map[string]int{}
	for _, project := range u.client.Projects() {
		at[project.ID] = len(rows)
		rows = append(rows, projectRow{id: project.ID, name: project.Name, tags: project.Tags})
	}
	none := projectRow{id: unfiled, name: "(none)"}

	since := float64(u.now().Add(-activeWindow).UnixNano()) / float64(time.Second)
	busiest := map[string]int{}
	for _, space := range u.spaces() {
		row := &none
		if index, ok := at[space.Project]; ok && space.Project != "" {
			row = &rows[index]
		}
		if space.Kind == "channel" {
			row.feeds++
		} else {
			row.rooms++
			if u.client.OpenInvitation(space.ID) {
				row.invited++
			}
		}
		if space.Open() {
			row.active++
		} else {
			row.closed++
		}
		// Only an open place counts as busy: a finished task room with a long
		// thread in it is a record, not work in progress.
		if said := u.client.Recent(space.ID, since); said > 0 && space.Open() {
			row.said += said
			if said > busiest[row.id] {
				busiest[row.id], row.busiest = said, space.Title
			}
		}
		// Unread only where the user has been, as the status line counts it,
		// or the columns would not add up to it. A channel's items are counted
		// only inside the channel.
		if u.client.Visited(space.ID) {
			row.unread += u.client.Unread(space.ID)
		}
		row.last = max(row.last, u.lastAt(space))
	}
	if none.rooms+none.feeds > 0 {
		rows = append(rows, none)
	}
	return rows
}

// overviewRows is the projects with the busiest open places, most said first.
// A project with nothing said lately is left out: the overview answers where
// the work is, and its absence is the answer for those.
func (u *Ui) overviewRows() []projectRow {
	rows := []projectRow{}
	for _, row := range u.projectRows() {
		if row.said > 0 {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].said != rows[j].said {
			return rows[i].said > rows[j].said
		}
		return rows[i].last > rows[j].last
	})
	return rows[:min(len(rows), overviewTop)]
}

// lastAt is when a space was last spoken in, or when it was founded.
func (u *Ui) lastAt(space client.Room) float64 {
	log := u.client.Log(space.ID)
	if len(log) > 0 {
		return log[len(log)-1].At
	}
	return space.CreatedAt
}

// roomRows is the rooms and channels the list shows: every one, or a single
// project's when the list was reached from the projects.
func (u *Ui) roomRows() []client.Room {
	rows := []client.Room{}
	for _, space := range u.places() {
		// A closed place is kept and readable; it is simply not what a list of
		// where the work is happening is for. `a` shows them.
		if space.Open() || u.showClosed {
			rows = append(rows, space)
		}
	}
	return rows
}

// places is every place the list covers, closed ones included.
func (u *Ui) places() []client.Room {
	rows := []client.Room{}
	for _, space := range u.spaces() {
		switch {
		case !u.scoped:
		case u.scope == unfiled && space.Project != "":
			continue
		case u.scope != unfiled && space.Project != u.scope:
			continue
		}
		rows = append(rows, space)
	}
	return rows
}

// closedCount is how many of them are closed.
func (u *Ui) closedCount() int {
	closed := 0
	for _, space := range u.places() {
		if !space.Open() {
			closed++
		}
	}
	return closed
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
		u.back()
	case tcell.KeyCtrlU:
		u.input = nil
	case tcell.KeyCtrlD:
		if len(u.input) == 0 {
			u.running = false
		}
	case tcell.KeyCtrlC:
		u.running = false
	case tcell.KeyRune:
		// With nothing typed, `a` shows the closed places too. Anywhere else
		// the rune is text, so the composer never loses a letter to a view.
		if event.Rune() == 'a' && len(u.input) == 0 &&
			(u.view == viewProject || u.view == viewRooms) {
			u.showClosed = !u.showClosed
			u.cursor = 0
			u.followCursor()
			return
		}
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
		// Enter on nothing typed goes one level in.
		u.enterRow()
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

// move steps the cursor: through a channel's items inside one, and through the
// rows of whichever list is on screen otherwise.
func (u *Ui) move(step int) {
	if u.view == viewSpace {
		space, ok := u.client.Space(u.selected)
		if !ok || !isFeed(space) {
			return
		}
		u.item = max(0, min(u.item+step, len(u.feedOrder(space))-1))
		return
	}
	u.cursor = max(0, min(u.cursor+step, u.rowCount()-1))
	switch u.view {
	case viewProject, viewRooms:
		u.followCursor()
	case viewPeople:
		if names := u.people(); len(names) > 0 {
			u.selectPerson(names[min(u.cursor, len(names)-1)])
		}
	}
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
		"projects": u.cmdProjects,
		"open":     u.cmdOpen, "meet": u.cmdMeet, "create": u.cmdCreate,
		"invite": u.cmdInvite, "uninvite": u.cmdUninvite, "leave": u.cmdLeave, "exit": u.cmdExit,
		"group": u.cmdGroup, "channel": u.cmdChannel, "project": u.cmdProject,
		"close": u.cmdClose, "reopen": u.cmdReopen,
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
			u.qualify(space), shape, len(space.Audience), client.Prefix(space.ID, 8)))
	}
	return nil
}

func (u *Ui) cmdProjects([]string) error {
	rows := u.projectRows()
	if len(rows) == 0 {
		u.notice("No projects yet")
	}
	for _, row := range rows {
		u.notice(fmt.Sprintf("%s -- %d rooms, %d channels", row.name, row.rooms, row.feeds))
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

// cmdCreate founds a permanent room. `/create <project>/<title>` founds it in
// that project, which is also where its title has to be free; the project on
// screen is the default. The rest of the line is the title, because a title
// may have spaces in it, so a task is named by filing afterwards.
func (u *Ui) cmdCreate(args []string) error {
	if len(args) == 0 {
		return refusal("A permanent room needs a name: /create [project/]<title>")
	}
	project, title, err := u.split(args[0])
	if err != nil {
		return err
	}
	if len(args) > 1 {
		title = strings.Join(append([]string{title}, args[1:]...), " ")
	}

	room, err := u.client.CreateRoom(title, nil, project, "", "")
	if err != nil {
		return err
	}
	u.notice("Founded " + u.qualify(room))
	u.enterRoom(room.ID)
	return nil
}

// split reads `<project>/<title>` into the two, taking the project on screen
// when none is named.
func (u *Ui) split(typed string) (project, title string, err error) {
	if name, rest, ok := strings.Cut(typed, "/"); ok {
		if project, err = u.projectID(name); err == nil {
			return project, strings.ReplaceAll(rest, "_", " "), nil
		}
	}
	if u.scoped && u.scope != unfiled {
		project = u.scope
	}
	return project, strings.ReplaceAll(typed, "_", " "), nil
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
	u.exited()
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

// cmdProject founds, files and dissolves. All three are administrator-only,
// and the server says so.
func (u *Ui) cmdProject(args []string) error {
	if len(args) == 0 {
		return refusal("Usage: /project new|tag|untag|file|rm ...")
	}
	switch action := strings.ToLower(args[0]); {
	// Trailing `#tag` words classify it, so the name may still have spaces.
	case action == "new" && len(args) >= 2:
		words, tags := splitTags(args[1:])
		project, err := u.client.CreateProject(strings.Join(words, " "), tags)
		if err != nil {
			return err
		}
		u.notice("Founded the project " + project.Name + describeTags(project.Tags))

	case (action == "tag" || action == "untag") && len(args) >= 3:
		project, err := u.projectID(args[1])
		if err != nil {
			return err
		}
		classify := u.client.Tag
		if action == "untag" {
			classify = u.client.Untag
		}
		for _, tag := range args[2:] {
			updated, err := classify(project, strings.TrimPrefix(tag, "#"))
			if err != nil {
				return err
			}
			u.notice(updated.Name + " is" + describeTags(updated.Tags))
		}

	// The room comes first because it is the subject; a missing project files
	// it under none, which is how a room leaves one. A trailing task label
	// makes it a task room.
	case action == "file" && len(args) >= 2:
		room, err := u.spaceID(args[1])
		if err != nil {
			return err
		}
		project, scope, task := "", "", ""
		if len(args) > 2 {
			if project, err = u.projectID(args[2]); err != nil {
				return err
			}
			if len(args) > 3 {
				scope, task = "task", strings.Join(args[3:], " ")
			}
		}
		filed, err := u.client.FileRoom(room, project, scope, task)
		if err != nil {
			return err
		}
		u.notice(u.qualify(filed) + " is " + u.describePlace(filed))

	case action == "rm" && len(args) >= 2:
		project, err := u.projectID(strings.Join(args[1:], " "))
		if err != nil {
			return err
		}
		name := strings.Join(args[1:], " ")
		if found := u.project(project); found != nil {
			name = found.Name
		}
		if err := u.client.DissolveProject(project); err != nil {
			return err
		}
		u.notice("Dissolved " + name + "; its rooms are filed under none")

	default:
		return refusal("Usage: /project new <name> [#tag]... |" +
			" tag|untag <name> <tag>... | file <room> [name] [task] | rm <name>")
	}
	return nil
}

// splitTags takes the trailing `#tag` words off a command's arguments.
func splitTags(args []string) (words, tags []string) {
	for _, arg := range args {
		if tag, ok := strings.CutPrefix(arg, "#"); ok {
			tags = append(tags, tag)
		} else {
			words = append(words, arg)
		}
	}
	return words, tags
}

func describeTags(tags []string) string {
	if len(tags) == 0 {
		return " with no tags"
	}
	return " tagged #" + strings.Join(tags, " #")
}

// qualify is how a place is referred to from outside its project:
// `<project>/<title>`. Inside the project the title alone is its name, so a
// place under none qualifies to itself.
func (u *Ui) qualify(space client.Room) string {
	if project := u.project(space.Project); project != nil {
		return project.Name + "/" + space.Title
	}
	return space.Title
}

// describePlace says where a place sits: its project, and what it is about.
func (u *Ui) describePlace(space client.Room) string {
	project := u.project(space.Project)
	if project == nil {
		return "under no project"
	}
	if space.Scope == "task" {
		return "task " + space.Task + " in " + project.Name
	}
	return "in " + project.Name
}

// cmdClose and cmdReopen say whether the work in a place is done. Naming no
// place means the one on screen.
func (u *Ui) cmdClose(args []string) error  { return u.setState(args, false) }
func (u *Ui) cmdReopen(args []string) error { return u.setState(args, true) }

func (u *Ui) setState(args []string, open bool) error {
	room, err := u.spaceID(strings.Join(args, " "))
	if err != nil {
		return err
	}
	updated, err := u.client.SetState(room, open)
	if err != nil {
		return err
	}
	if open {
		u.notice(u.qualify(updated) + " is open again")
	} else {
		u.notice(u.qualify(updated) + " is closed; it is still here and still read")
	}
	return nil
}

// projectID resolves what was typed to a project: its name, without case, or
// the start of its id.
func (u *Ui) projectID(typed string) (string, error) {
	var byName, byPrefix []string
	for _, project := range u.client.Projects() {
		if strings.EqualFold(project.Name, typed) ||
			strings.EqualFold(strings.ReplaceAll(project.Name, " ", "_"), typed) {
			byName = append(byName, project.ID)
		}
		if strings.HasPrefix(project.ID, typed) {
			byPrefix = append(byPrefix, project.ID)
		}
	}
	for _, found := range [][]string{byName, byPrefix} {
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return "", refusal(fmt.Sprintf("'%s' names more than one project: give more of its id", typed))
		}
	}
	return "", refusal(fmt.Sprintf("No such project: %s", typed))
}

// spaceID resolves what was typed to a room or a channel: `<project>/<title>`
// from anywhere, a bare title within the project on screen or across all of
// them, or the start of an id. Nothing typed means the place on screen.
//
// A title may hold a slash, so the split is at the first one and only when
// what precedes it names a project. `cynn/task/31` is task/31 in cynn.
func (u *Ui) spaceID(typed string) (string, error) {
	if typed == "" {
		if u.selected == "" {
			return "", refusal("Name a room, or highlight one first")
		}
		return u.selected, nil
	}

	// Qualified: the project is named, so only its own places are searched.
	if name, title, ok := strings.Cut(typed, "/"); ok {
		if project, err := u.projectID(name); err == nil {
			return u.matchTitle(title, typed, func(space client.Room) bool {
				return space.Project == project
			})
		}
	}
	// Bare: the project on screen first, so a name inside it is enough.
	if u.scoped && u.scope != unfiled {
		if id, err := u.matchTitle(typed, typed, func(space client.Room) bool {
			return space.Project == u.scope
		}); err == nil {
			return id, nil
		}
	}
	return u.matchTitle(typed, typed, func(client.Room) bool { return true })
}

// matchTitle finds the one place among those where covers whose title, or id,
// is what was typed. typed is what the refusal quotes back.
func (u *Ui) matchTitle(title, typed string, where func(client.Room) bool) (string, error) {
	var byTitle, byPrefix []string
	for _, space := range u.spaces() {
		if !where(space) {
			continue
		}
		if strings.EqualFold(space.Title, title) ||
			strings.EqualFold(strings.ReplaceAll(space.Title, " ", "_"), title) {
			byTitle = append(byTitle, space.ID)
		}
		if strings.HasPrefix(space.ID, title) {
			byPrefix = append(byPrefix, space.ID)
		}
	}
	for _, found := range [][]string{byTitle, byPrefix} {
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return "", refusal(fmt.Sprintf(
				"'%s' names more than one place: give its project, as project/%s", typed, title))
		}
	}
	return "", refusal(fmt.Sprintf("No such room or channel: %s", typed))
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
		if message.Author != "system" || message.Kind != "event" {
			continue
		}
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
//
// The founder is one word, a username. A file event puts a path there instead,
// with spaces in it, and a path can be named to look like an announcement.
func announced(line string) (id, title string, ok bool) {
	founder, rest, found := strings.Cut(line, " opened the channel ")
	open := strings.LastIndex(rest, " (")
	if !found || strings.ContainsAny(founder, " \t") || open < 0 || !strings.HasSuffix(rest, ")") {
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

	if len(words) == 0 {
		return refusal("A channel needs a name")
	}
	project, title, err := u.split(words[0])
	if err != nil {
		return err
	}
	if len(words) > 1 {
		title = strings.Join(append([]string{title}, words[1:]...), " ")
	}

	channel, err := u.client.CreateChannel(title, groups, project)
	if err != nil {
		return err
	}
	rule := "restricted"
	if len(groups) == 0 {
		rule = "open to everybody"
	}
	u.notice(fmt.Sprintf("Opened %s (%s), %s", u.qualify(channel), channel.ID, rule))
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

	// Inside a room the screen is that room: no tab bar over it.
	top := 1
	if u.occupancy == "" && u.results == nil {
		u.drawTabs(width)
		top = 2
	}
	// The body first: it marks what is on screen read, and the status counts it.
	u.drawBody(top, height, width)
	u.drawStatus(width)
	u.drawComposer(height, width)
	u.screen.Show()
}

// put draws text into a field, optionally clearing the rest of it. Widths are
// terminal cells, so a character two cells wide takes two.
//
// A control character is drawn as '?': a body is another user's text, and an
// escape sequence written raw would be obeyed by the terminal. So is a bidi
// control, which would reorder what the rest of the line appears to say.
func (u *Ui) put(y, x int, text string, width int, style tcell.Style, pad bool) {
	if y < 0 || x < 0 || width <= 0 {
		return
	}
	used := 0
	for rest := printable(text); rest != ""; {
		cluster, after, cells, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		if used+cells > width {
			break
		}
		if cells > 0 {
			u.screen.Put(x+used, y, cluster, style)
		}
		used, rest = used+cells, after
	}
	for pad && used < width {
		u.screen.SetContent(x+used, y, ' ', nil, style)
		used++
	}
}

func printable(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return '?'
		}
		return r
	}, text)
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
		who += "  in " + u.qualify(space)
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
	u.put(0, 0, ljust(" "+who, width), width, reverse, false)
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

// drawTabs is the tab bar: the header of every screen outside a room. The
// current tab is drawn again over itself, because only its own cells change.
func (u *Ui) drawTabs(width int) {
	dim := u.tint(plain, "dim")
	bar, at, current := " minos  ", 0, ""
	for _, entry := range tabs {
		label := " " + entry.String() + " "
		if entry == u.tab {
			at, current = len(bar), label
		}
		bar += label + " "
	}
	u.put(1, 0, ljust(bar, width), width, dim, true)
	u.put(1, at, current, cells(current), plain.Bold(true).Underline(true), false)

	if summary := u.summary(); summary != "" {
		u.put(1, max(len(bar), width-cells(summary)-1), summary, cells(summary), dim, false)
	}
}

// summary is the count at the right of the tab bar, naming what the list holds.
func (u *Ui) summary() string {
	switch u.view {
	case viewOverview:
		return fmt.Sprintf("%d of %s busy", len(u.overviewRows()),
			plural(len(u.client.Projects()), "project"))
	case viewProjects:
		return plural(len(u.client.Projects()), "project")
	case viewProject:
		// The page's own stats line carries the counts; repeating them here
		// would say the same thing twice on one screen.
		return u.scopeName()
	case viewRooms:
		return u.placeSummary()
	case viewPeople:
		return plural(len(u.people()), "person")
	}
	return ""
}

// placeSummary counts what a list of places shows, and what it is hiding.
func (u *Ui) placeSummary() string {
	summary := plural(len(u.roomRows()), "place")
	if closed := u.closedCount(); closed > 0 {
		summary += fmt.Sprintf("  %d closed", closed)
	}
	return summary
}

// scopeName names the project the rooms list is limited to.
func (u *Ui) scopeName() string {
	if u.scope == unfiled {
		return "under no project"
	}
	if project := u.project(u.scope); project != nil {
		return project.Name
	}
	return ""
}

func plural(n int, thing string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", thing)
	}
	if thing == "person" {
		return fmt.Sprintf("%d people", n)
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// drawBody draws whichever screen is current, between the tab bar and the
// composer.
func (u *Ui) drawBody(top, height, width int) {
	bottom := height - 3
	if u.results != nil {
		u.drawResults(top, width, bottom)
		return
	}
	switch u.view {
	case viewOverview:
		u.drawOverview(top, width, bottom)
	case viewProjects:
		u.drawProjects(top, width, bottom)
	case viewProject:
		u.drawProject(top, width, bottom)
	case viewRooms:
		u.drawRooms(top, width, bottom)
	case viewPeople:
		u.drawPeople(top, width, bottom)
	default:
		u.drawPane(top, width, bottom)
	}
}

// drawTable lays out a table under the tab bar and draws the rows around the
// cursor, styling each by what style returns for its row.
func (u *Ui) drawTable(top, width, bottom int, t *table, style func(int) tcell.Style, empty string) {
	t.layout(width - 1)
	u.put(top+1, 1, t.header(), width-1, u.tint(plain, "dim"), true)
	if len(t.rows) == 0 {
		u.put(top+2, 1, empty, width-1, u.tint(plain, "dim"), true)
		return
	}

	// Scrolled so the row under the cursor is on screen.
	visible := max(1, bottom-(top+2))
	start := max(0, min(u.cursor-visible/2, len(t.rows)-visible))
	for offset := 0; offset < visible && start+offset < len(t.rows); offset++ {
		row := start + offset
		marked := row == u.cursor
		rowStyle := style(row)
		if marked {
			rowStyle = rowStyle.Bold(true)
		}
		u.put(top+2+offset, 1, t.line(row, marked), width-1, rowStyle, true)
	}
}

// drawOverview is where the work is: the busiest projects, and then the counts
// that say what is waiting on this user.
func (u *Ui) drawOverview(top, width, bottom int) {
	rows := u.overviewRows()
	t := &table{cols: []column{
		{title: "PROJECT", shrink: 2, min: 8},
		{title: "TAGS", shrink: 4, min: 6},
		{title: "ACTIVE", right: true},
		{title: "SAID", right: true},
		{title: "BUSIEST", shrink: 3, min: 8},
		{title: "LAST", right: true, shrink: 5, min: 5},
	}}
	for _, row := range rows {
		t.add(row.name, strings.Join(row.tags, " "), fmt.Sprint(row.active),
			fmt.Sprint(row.said), row.busiest, u.ago(row.last))
	}

	// The table takes what it needs; the counts sit under it.
	health := u.health()
	u.drawTable(top, width, bottom-len(health)-1, t, func(index int) tcell.Style {
		if rows[index].unread > 0 || rows[index].invited > 0 {
			return u.tint(plain, "me")
		}
		return plain
	}, fmt.Sprintf("(nothing said in the last %d days)", int(activeWindow.Hours()/24)))

	for offset, text := range health {
		u.put(bottom-len(health)+offset, 1, text, width-1, u.tint(plain, "dim"), true)
	}
}

// health is the two lines under the overview: what there is, and what is
// waiting on this user.
func (u *Ui) health() []string {
	open, closed, feeds := 0, 0, 0
	for _, space := range u.spaces() {
		switch {
		case !space.Open():
			closed++
		case space.Kind == "channel":
			feeds++
			open++
		default:
			open++
		}
	}
	what := fmt.Sprintf("%s  ·  %s open, %d of them channels",
		plural(len(u.client.Projects()), "project"), plural(open, "place"), feeds)
	if closed > 0 {
		what += fmt.Sprintf("  ·  %d closed", closed)
	}

	waiting := []string{}
	if invitations := u.client.OpenInvitations(); invitations > 0 {
		waiting = append(waiting, plural(invitations, "open invitation"))
	}
	if unread := u.client.UnreadMessages(); unread > 0 {
		waiting = append(waiting, plural(int(unread), "unread message"))
	}
	if pending := len(u.client.Queued()); pending > 0 {
		waiting = append(waiting, plural(pending, "submission")+" to decide")
	}
	if len(waiting) == 0 {
		waiting = append(waiting, "nothing waiting on you")
	}
	return []string{what, strings.Join(waiting, "  ·  ")}
}

// ago is how long since a moment, in the shortest form that says it.
func (u *Ui) ago(at float64) string {
	if at <= 0 {
		return ""
	}
	since := u.now().Sub(time.Unix(0, int64(at*float64(time.Second))))
	switch {
	case since < time.Minute:
		return "just now"
	case since < time.Hour:
		return fmt.Sprintf("%dm ago", int(since.Minutes()))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(since.Hours()))
	case since < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(since.Hours()/24))
	}
	return stamp(at, "02 Jan")
}

// drawProject is one project's own page: what it is, then its places.
func (u *Ui) drawProject(top, width, bottom int) {
	name, tags := u.scopeName(), ""
	if project := u.project(u.scope); project != nil && len(project.Tags) > 0 {
		tags = "  #" + strings.Join(project.Tags, " #")
	}
	u.put(top, 1, name+tags, width-1, plain.Bold(true), true)

	var row projectRow
	for _, candidate := range u.projectRows() {
		if candidate.id == u.scope {
			row = candidate
		}
	}
	stats := fmt.Sprintf("%d open of %s", row.active, plural(row.rooms+row.feeds, "place"))
	if row.closed > 0 {
		stats += fmt.Sprintf(", %d closed", row.closed)
	}
	if row.feeds > 0 {
		stats += fmt.Sprintf("  ·  %s", plural(row.feeds, "channel"))
	}
	stats += fmt.Sprintf("  ·  %s in the last %d days",
		plural(row.said, "message"), int(activeWindow.Hours()/24))
	u.put(top+1, 1, stats, width-1, u.tint(plain, "dim"), true)

	u.drawPlaces(top+2, width, bottom)
}

// drawProjects is the projects list: a table of containers, not of places.
func (u *Ui) drawProjects(top, width, bottom int) {
	rows := u.projectRows()
	t := &table{cols: []column{
		{title: "PROJECT", shrink: 2, min: 8},
		{title: "TAGS", shrink: 5, min: 6},
		{title: "ACTIVE", right: true},
		{title: "ROOMS", right: true},
		{title: "CHANNELS", right: true},
		{title: "UNREAD", right: true},
		{title: "LAST", right: true, shrink: 4, min: 5},
	}}
	for _, row := range rows {
		unread := ""
		switch {
		case row.invited > 0 && row.unread > 0:
			unread = fmt.Sprintf("%d +%d new", row.unread, row.invited)
		case row.invited > 0:
			unread = fmt.Sprintf("%d new", row.invited)
		case row.unread > 0:
			unread = fmt.Sprintf("%d", row.unread)
		}
		t.add(row.name, strings.Join(row.tags, " "), fmt.Sprint(row.active),
			fmt.Sprint(row.rooms), fmt.Sprint(row.feeds), unread, u.ago(row.last))
	}

	u.drawTable(top, width, bottom, t, func(index int) tcell.Style {
		if rows[index].unread > 0 || rows[index].invited > 0 {
			return u.tint(plain, "me")
		}
		return plain
	}, "(no projects yet; /project new <name> founds one)")
}

// drawRooms is the flat list of places, filed or not. A project's own page
// draws the same table over its own places.
func (u *Ui) drawRooms(top, width, bottom int) { u.drawPlaces(top, width, bottom) }

func (u *Ui) drawPlaces(top, width, bottom int) {
	rows := u.roomRows()
	ambiguous := u.ambiguousTitles(rows)
	since := float64(u.now().Add(-activeWindow).UnixNano()) / float64(time.Second)

	// The project column says nothing on a project's own page, and the table
	// drops a column with no text, so it is left empty there rather than cut.
	t := &table{cols: []column{
		{title: "PLACE", shrink: 2, min: 8},
		{title: "KIND"},
		{title: "PROJECT", shrink: 5, min: 6},
		{title: "SCOPE", shrink: 6, min: 4},
		{title: "TASK", shrink: 4, min: 4},
		{title: "SAID", right: true},
		{title: "STATE", shrink: 3, min: 7},
	}}
	for _, space := range rows {
		label := space.Title
		if ambiguous[space.ID] {
			label += stamp(space.CreatedAt, " 15:04:05")
		}
		kind := space.Kind
		if space.Kind == "room" && space.Retention == "transient" {
			kind = "room ~"
		}
		project := ""
		if found := u.project(space.Project); found != nil && u.view != viewProject {
			project = found.Name
		}
		said := ""
		if count := u.client.Recent(space.ID, since); count > 0 {
			said = fmt.Sprint(count)
		}
		t.add(label, kind, project, space.Scope, space.Task, said, u.placeState(space))
	}

	empty := "(nothing here; /open <user> raises a room)"
	if u.closedCount() > 0 {
		empty = "(nothing open here; a shows the closed ones)"
	}
	u.drawTable(top, width, bottom, t, func(index int) tcell.Style {
		space := rows[index]
		switch {
		case !space.Open():
			return u.tint(plain, "dim")
		case u.client.OpenInvitation(space.ID),
			u.client.Visited(space.ID) && u.client.Unread(space.ID) > 0:
			return u.tint(plain, "me")
		}
		return plain
	}, empty)
}

// placeState is the one thing most worth saying about a place right now.
func (u *Ui) placeState(space client.Room) string {
	switch {
	case !space.Open():
		return "closed"
	case space.Kind == "room" && u.client.OpenInvitation(space.ID):
		// Not yet entered, so its messages are not unread: they have never
		// been seen.
		return "invited"
	case u.client.Visited(space.ID) && u.client.Unread(space.ID) > 0:
		return fmt.Sprintf("%d unread", u.client.Unread(space.ID))
	case len(space.Occupants) > 0:
		return fmt.Sprintf("%d here", len(space.Occupants))
	}
	return ""
}

// drawPeople is the roster, and what Enter does to a row of it.
func (u *Ui) drawPeople(top, width, bottom int) {
	names := u.people()
	online := map[string]bool{}
	for _, user := range u.client.Users() {
		online[user.Username] = user.Online
	}
	shared := map[string][]string{}
	for _, space := range u.spaces() {
		if space.Kind != "room" {
			continue
		}
		for _, name := range space.Audience {
			if name != u.client.Me() {
				shared[name] = append(shared[name], u.qualify(space))
			}
		}
	}

	t := &table{cols: []column{
		{title: "PERSON", shrink: 2, min: 6},
		{title: "STATE"},
		{title: "ROOMS SHARED", shrink: 3, min: 10},
	}}
	for _, name := range names {
		state := "offline"
		if online[name] {
			state = "online"
		}
		t.add(name, state, strings.Join(shared[name], "; "))
	}

	u.drawTable(top, width, bottom, t, func(index int) tcell.Style {
		if online[names[index]] {
			return u.tint(plain, "good")
		}
		return u.tint(plain, "dim")
	}, "(nobody else here)")
}

// ambiguousTitles is the places a row's own columns cannot tell apart: same
// title, same project. Two rooms called `design` in different projects are not
// among them, because the project column says which is which. What is left is
// ad-hoc rooms, and when each began is how anybody would tell those apart.
func (u *Ui) ambiguousTitles(spaces []client.Room) map[string]bool {
	seen, twice := map[string]bool{}, map[string]bool{}
	for _, space := range spaces {
		name := u.qualify(space)
		if seen[name] {
			twice[space.ID] = true
			for _, other := range spaces {
				if u.qualify(other) == name {
					twice[other.ID] = true
				}
			}
		}
		seen[name] = true
	}
	return twice
}

type line struct {
	text  string
	style tcell.Style
}

// drawPane is one space: its header, then its log or its items.
func (u *Ui) drawPane(top, width, bottom int) {
	left, pane := 1, width-1

	space, ok := u.client.Space(u.selected)
	if u.selected == "" || !ok {
		u.put(top, left, "No room selected. /open <user> to raise one,", pane, plain, false)
		u.put(top+1, left, "or /help for what else there is.", pane, plain, false)
		return
	}

	header := u.qualify(space)
	if space.Kind == "channel" {
		header += "  (channel: read-only)"
	} else if space.Retention == "transient" {
		header += "  (transient: discarded when everyone leaves)"
	}
	u.put(top, left, header, pane, plain.Bold(true), true)
	who := fmt.Sprintf("%d invited", len(space.Audience))
	if len(space.Occupants) > 0 {
		who += ", here now: " + strings.Join(space.Occupants, ", ")
	}
	if space.Scope == "task" {
		who += "  (task " + space.Task + ")"
	}
	u.put(top+1, left, who, pane, u.tint(plain, "dim"), true)

	if isFeed(space) {
		u.drawFeed(space, top, pane, bottom)
		return
	}

	lines := u.paneLines(space, pane)
	visible := max(0, bottom-(top+2))
	u.scroll = max(0, min(u.scroll, max(0, len(lines)-visible)))
	end := len(lines) - u.scroll
	for offset, line := range lines[max(0, end-visible):end] {
		u.put(top+2+offset, left, line.text, pane, line.style, true)
	}

	// Only a room has a read cursor, and only one the user is in counts as seen: a
	// highlighted room is a preview.
	if u.scroll == 0 && space.Kind == "room" && u.occupancy != "" {
		u.markRead(space)
	}
}

// drawFeed draws a channel as its items: the pending ones, then the opened ones,
// each a subject line, with the body shown under the one that is expanded.
func (u *Ui) drawFeed(space client.Room, top, pane, bottom int) {
	left := 1
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
	visible := max(0, bottom-(top+2))
	start := max(0, min(focusEnd-visible+1, focus))
	for offset, line := range lines[start:min(len(lines), start+visible)] {
		u.put(top+2+offset, left, line.text, pane, line.style, true)
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
		indent := uniseg.StringWidth(prefix)
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
func (u *Ui) drawResults(top, width, bottom int) {
	left, pane := 1, width-1
	u.put(top, left, u.resultsTitle, pane, plain.Bold(true), true)
	u.put(top+1, left, "Esc: back to the conversation", pane, u.tint(plain, "dim"), true)

	lines := make([]line, 0, len(u.results))
	for _, text := range u.results {
		lines = append(lines, line{text, plain})
	}
	lines = append(lines, u.sessionLines(pane)...)
	visible := max(0, bottom-(top+2))
	u.scroll = max(0, min(u.scroll, max(0, len(lines)-visible)))
	end := len(lines) - u.scroll
	for offset, line := range lines[max(0, end-visible):end] {
		u.put(top+2+offset, left, line.text, pane, line.style, true)
	}
}

// markRead moves the read cursor when the newest message is on screen. Seen, as
// opposed to received, and kept by the server because it is the same from every
// device. It runs while drawing, so the server is told without waiting.
func (u *Ui) markRead(space client.Room) {
	u.client.MarkReadLater(space.ID, space.LastSeq)
}

func (u *Ui) drawComposer(height, width int) {
	space, ok := u.client.Space(u.selected)
	readOnly := ok && space.Kind == "channel"

	u.put(height-3, 0, strings.Repeat("-", width), width, u.tint(plain, "dim"), false)
	prompt := "> "
	hint := " /help  Tab: next tab  ^C: quit"
	switch u.view {
	case viewOverview:
		hint = " Up/Down: project  Enter: open it  Tab: next tab  /help  ^C: quit"
	case viewProjects:
		hint = " Up/Down: project  Enter: open it  Tab: next tab  /help  ^C: quit"
	case viewProject:
		hint = " Up/Down: place  Enter: go in  a: closed too  Esc: back  ^C: quit"
	case viewPeople:
		if u.person != "" {
			prompt = "  (to " + u.person + ") "
			hint = " Enter: open a room with " + u.person + "  Up/Down: person  Tab: next tab  ^C: quit"
		}
	case viewRooms:
		hint = " Up/Down: place  Enter: go in  a: closed too  Tab: next tab  ^C: quit"
	case viewSpace:
		switch {
		case u.submitting(u.selected):
			prompt = "  (submit) "
		case readOnly:
			prompt = "  (channel) "
		}
		switch {
		case ok && isFeed(space):
			hint = " Up/Down: item  Enter: open or close  subject | body  Esc: back  ^C: quit"
		case ok && u.occupancy != "":
			hint = " /exit: step out  /invite <who>  /leave  /help  PgUp/PgDn: scroll  ^C: quit"
		default:
			hint = " Esc: back  PgUp/PgDn: scroll  /help  ^C: quit"
		}
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
	u.screen.ShowCursor(min(uniseg.StringWidth(printable(text)), width-1), height-2)
}

// -- text --------------------------------------------------------------------

func stamp(at float64, layout string) string {
	return time.Unix(0, int64(at*float64(time.Second))).Local().Format(layout)
}

func ljust(text string, width int) string {
	if gap := width - uniseg.StringWidth(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// wrap fills lines of at most width cells the way Python's textwrap does:
// whitespace collapses, and a word longer than a line fills what is left of it.
func wrap(text string, width int) []string {
	var lines []string
	var current strings.Builder
	used := 0
	for _, word := range strings.Fields(text) {
		for word != "" {
			gap := min(used, 1)
			cells := uniseg.StringWidth(word)
			if used+gap+cells <= width {
				if gap == 1 {
					current.WriteByte(' ')
				}
				current.WriteString(word)
				used += gap + cells
				break
			}
			if cells > width {
				head, tail := cut(word, width-used-gap)
				if head == "" && used == 0 {
					// Narrower than one character: it has to go somewhere.
					head, tail, _, _ = uniseg.FirstGraphemeClusterInString(word, -1)
				}
				if head != "" {
					if gap == 1 {
						current.WriteByte(' ')
					}
					current.WriteString(head)
					word = tail
				}
			}
			lines = append(lines, current.String())
			current.Reset()
			used = 0
		}
	}
	if used > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

// cut is the longest run of whole characters from the start of text that fits
// in cells, and what is left.
func cut(text string, cells int) (string, string) {
	used := 0
	for rest := text; rest != ""; {
		_, after, width, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		if used+width > cells {
			return text[:len(text)-len(rest)], rest
		}
		used, rest = used+width, after
	}
	return text, ""
}
