package tui

// The command layer and the composer against a real server. No screen: these
// are where a keystroke reaches the server, and drawing is not.

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

func connect(t *testing.T, base, username string) *client.Client {
	t.Helper()
	_, c, _, err := client.Connect(base, username, username)
	if err != nil {
		t.Fatalf("%s cannot connect: %v", username, err)
	}
	t.Cleanup(c.Stop)
	return c
}

func waitFor(t *testing.T, what string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// headless is the interface without a terminal under it.
func headless(c *client.Client) *Ui {
	return newUi(nil, c, client.Profile{Username: c.Me()})
}

// said is the first notice containing fragment, and whether there was one.
func (u *Ui) said(fragment string) (string, bool) {
	for _, notice := range u.recentNotices(noticeKeep) {
		if strings.Contains(notice, fragment) {
			return notice, true
		}
	}
	return "", false
}

func (u *Ui) mustSay(t *testing.T, fragment string) string {
	t.Helper()
	notice, ok := u.said(fragment)
	if !ok {
		t.Fatalf("no notice says %q; got %q", fragment, u.recentNotices(noticeKeep))
	}
	return notice
}

func (u *Ui) compose(text string) {
	u.input = []rune(text)
	u.submit()
}

func TestASlashCommandRaisesARoomAndSelectsIt(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.command("/open alice")

	room, ok := demo.Rooms()[ui.selected]
	if !ok || !slices.Equal(room.Audience, []string{"alice", "demo"}) {
		t.Fatalf("selected %q, room %+v", ui.selected, room)
	}
}

func TestMeetRaisesATransientRoomAndSaysSo(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.command("/meet alice")

	if room := demo.Rooms()[ui.selected]; room.Retention != "transient" {
		t.Fatalf("raised %+v", room)
	}
	ui.mustSay(t, "discarded")
}

func TestAnAtPrefixNamesAGroup(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	if _, err := demo.CreateGroup("Team", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	ui := headless(demo)
	ui.command("/create Team room")
	ui.command("/invite @Team")

	if room := demo.Rooms()[ui.selected]; !slices.Equal(room.Audience, []string{"alice", "demo"}) {
		t.Fatalf("audience is %v; notices %q", room.Audience, ui.recentNotices(noticeKeep))
	}
}

func TestAnUnknownGroupIsRefusedByName(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.command("/open @nobody")
	ui.mustSay(t, "No such group")
}

// Help is in the pane, where all of it fits, and names every command there is.
func TestHelpShowsEveryCommand(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.command("/help")

	if ui.resultsTitle != "Commands" || len(ui.results) != 2*len(help) {
		t.Fatalf("help shows %q with %d lines", ui.resultsTitle, len(ui.results))
	}
	listed := strings.Join(ui.results, "\n")
	for name := range ui.commands() {
		if !strings.Contains(listed, "/"+name) {
			t.Errorf("/help does not mention /%s", name)
		}
	}
}

func TestAnUnknownCommandSuggestsHelp(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.command("/merge")
	ui.mustSay(t, "/help")
}

// An ordinary user cannot found a permanent room, and is told why.
func TestARefusalFromTheServerReachesTheNotices(t *testing.T) {
	alice := connect(t, testserver.Start(t, config.HistoryLimit).Base, "alice")
	ui := headless(alice)
	ui.command("/create Engineering")
	ui.mustSay(t, "administrator")
}

// The admin's half of a channel: who may subscribe, not who is subscribed.
func TestChannelAdmitAndRevokeMoveTheAudienceRule(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	if _, err := demo.CreateGroup("Ops", []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	ui := headless(demo)

	ui.command("/channel admit system Ops")
	ui.mustSay(t, "may subscribe")
	// Alice is subscribed and in no admitted group, so her client is told to drop
	// the channel: the rule reaches people who never asked for anything.
	waitFor(t, "alice to lose the channel", func() bool {
		_, ok := alice.Space("system")
		return !ok
	})

	ui.command("/channel revoke system Ops")
	ui.mustSay(t, "open to everybody")
}

func TestARestrictedChannelRefusesAnOutsider(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	if _, err := demo.CreateGroup("Ops", []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	headless(demo).command("/channel admit system Ops")

	if err := alice.Unsubscribe("system"); err != nil {
		t.Fatalf("alice cannot unsubscribe: %v", err)
	}
	ui := headless(alice)
	ui.command("/subscribe system")
	ui.mustSay(t, "restricted")
}

func TestAnAnnouncementNamesTheChannelItFounded(t *testing.T) {
	id, title, ok := announced("demo opened the channel Ops (east) (4f1c-9a)")
	if !ok || id != "4f1c-9a" || title != "Ops (east)" {
		t.Fatalf("read %q, %q, %v", id, title, ok)
	}
	if _, _, ok := announced("demo wrote home:/notes.txt"); ok {
		t.Fatal("a file event was read as an announcement")
	}
}

// A channel is one word: its title with _ for a space, which system announced
// before anyone could subscribe, or the start of its id as /rooms prints it.
func TestAChannelIsNamedByTitleOrIdPrefix(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	founded, err := demo.CreateChannel("Ops notices", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the announcement", func() bool {
		return slices.ContainsFunc(alice.Log(systemChannel), func(m client.Message) bool {
			return strings.Contains(m.Body, "opened the channel Ops notices")
		})
	})

	reader := headless(alice)
	// More than one word is not guessed at.
	reader.command("/subscribe ops notices")
	reader.mustSay(t, "Usage: /subscribe <channel>")
	reader.command("/subscribe ops_NOTICES")
	if _, ok := alice.Channels()[founded.ID]; !ok || reader.selected != founded.ID {
		t.Fatalf("alice did not subscribe by name; notices %q", reader.recentNotices(noticeKeep))
	}

	ui := headless(demo)
	ui.command("/channel appoint Ops notices alice")
	ui.mustSay(t, "Usage: /channel")
	ui.command("/channel appoint Ops_notices alice")
	ui.mustSay(t, "alice moderates Ops notices")
	ui.command("/channel dismiss " + founded.ID[:8] + " alice")
	ui.mustSay(t, "no moderator left")

	// What resolves to nothing goes to the server, which says so.
	reader.command("/subscribe nosuch")
	reader.mustSay(t, "No such room: nosuch")
}

// The composer takes the producer's path when the space is a channel.
func TestAChannelIsFoundedAndWrittenToFromTheComposer(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	ui := headless(demo)
	ui.command("/channel new Announcements")
	opened := ui.mustSay(t, "Opened Announcements")
	channel := opened[strings.Index(opened, "(")+1 : strings.Index(opened, ")")]

	// Founding a channel is not subscribing to one: an admin who wants to write
	// in the composer takes their place in the audience like anybody else.
	for _, c := range []*client.Client{demo, alice} {
		if _, err := c.Subscribe(channel); err != nil {
			t.Fatal(err)
		}
	}
	ui.selectSpace(channel)
	ui.compose("the first")

	waitFor(t, "the publication", func() bool { return len(alice.Log(channel)) > 0 })
	if last := alice.Log(channel)[0]; last.Body != "the first" || last.Author != "demo" {
		t.Fatalf("alice received %+v", last)
	}
}

func TestAnOrdinaryUserCannotPublishToAChannel(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	headless(demo).command("/channel new Bulletins")

	ui := headless(alice)
	ui.selectSpace("system")
	ui.compose("hello")
	ui.mustSay(t, "administrator")
}

// collector gathers what a client says, from whichever goroutine says it.
type collector struct {
	mutex sync.Mutex
	lines []string
}

func (c *collector) add(text string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lines = append(c.lines, text)
}

func (c *collector) has(fragment string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return slices.ContainsFunc(c.lines, func(line string) bool { return strings.Contains(line, fragment) })
}

// Tab walks past the spaces to the people, then wraps back to the spaces.
func TestTabReachesPeopleAfterTheSpaces(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.ensureSelection()

	walk := []string{ui.selected + "|" + ui.person}
	for range 3 {
		ui.cycle(1)
		walk = append(walk, ui.selected+"|"+ui.person)
	}
	if want := []string{"system|", "|alice", "|bob", "system|"}; !slices.Equal(walk, want) {
		t.Fatalf("Tab walked %q, want %q", walk, want)
	}
	ui.cycle(-1)
	if ui.person != "bob" || ui.selected != "" {
		t.Fatalf("S-Tab reached %q|%q", ui.selected, ui.person)
	}
}

func TestEnterOnAPersonRaisesARoomWithThemAndEntersIt(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.selectPerson("alice")
	ui.compose("")

	room, ok := demo.Rooms()[ui.selected]
	if !ok || ui.person != "" || !slices.Equal(room.Audience, []string{"alice", "demo"}) {
		t.Fatalf("selected %q|%q, room %+v", ui.selected, ui.person, room)
	}
	if ui.occupancy == "" {
		t.Fatal("the new room was selected without being entered")
	}
}

func TestTypingToAPersonSendsTheFirstMessage(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	ui := headless(demo)
	ui.selectPerson("alice")
	ui.compose("lunch?")

	room := ui.selected
	waitFor(t, "alice to receive it", func() bool {
		return slices.ContainsFunc(alice.Log(room), func(m client.Message) bool {
			return m.Body == "lunch?" && m.Author == "demo"
		})
	})
}

// -- being in a room --------------------------------------------------------------

// Highlighting a room is not entering it; Enter goes in, and then Tab stays put
// until the user steps out.
func TestEnterGoesIntoARoomAndExitStepsOut(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo := connect(t, server.Base, "demo")
	room, err := demo.OpenRoom([]client.Principal{{Kind: "user", ID: "alice"}}, "Standup", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	ui := headless(demo)
	ui.ensureSelection()
	if ui.selected != room.ID || ui.occupancy != "" || len(server.Store.OccupantsOf(room.ID)) != 0 {
		t.Fatalf("highlighting entered the room: selected %q, occupancy %q", ui.selected, ui.occupancy)
	}

	ui.compose("")
	if ui.occupancy == "" || !slices.Equal(server.Store.OccupantsOf(room.ID), []string{"demo"}) {
		t.Fatalf("Enter did not go in: occupants %v", server.Store.OccupantsOf(room.ID))
	}
	ui.cycle(1)
	if ui.selected != room.ID {
		t.Fatalf("Tab left the room for %q", ui.selected)
	}

	ui.command("/exit")
	if ui.occupancy != "" || ui.selected != room.ID || len(server.Store.OccupantsOf(room.ID)) != 0 {
		t.Fatalf("/exit left occupancy %q, occupants %v", ui.occupancy, server.Store.OccupantsOf(room.ID))
	}
	ui.command("/exit")
	ui.mustSay(t, "not in a room")
}

// An invitation is offered, not entered, and waits while the user is in a room.
func TestAnInvitationIsOfferedAndHeldWhileInARoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	invite := []client.Principal{{Kind: "user", ID: "alice"}}
	ui := headless(alice)

	first, err := demo.OpenRoom(invite, "First", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first invitation", func() bool { _, ok := alice.Rooms()[first.ID]; return ok })
	ui.watch()
	ui.mustSay(t, "You were invited to First")
	if ui.occupancy != "" {
		t.Fatal("an invitation pulled alice into the room")
	}

	ui.enterRoom(first.ID)
	second, err := demo.OpenRoom(invite, "Second", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second invitation", func() bool { _, ok := alice.Rooms()[second.ID]; return ok })
	ui.watch()
	if _, said := ui.said("invited to Second"); said || ui.elsewhere() != 1 {
		t.Fatalf("in a room, said %v and counted %d elsewhere", said, ui.elsewhere())
	}
	ui.command("/exit")
	ui.mustSay(t, "invited to Second")
}

// Entering a room on another device is leaving this one, and the zoom ends.
func TestEnteringElsewhereEndsTheZoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	laptop, phone := connect(t, server.Base, "alice"), connect(t, server.Base, "alice")
	here, err := laptop.OpenRoom(nil, "Here", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	there, err := laptop.OpenRoom(nil, "There", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	ui := headless(laptop)
	ui.enterRoom(here.ID)
	held := ui.occupancy

	if _, err := phone.Enter(there.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the release", func() bool { return !laptop.Holds(held) })
	ui.watch()
	if ui.occupancy != "" {
		t.Fatal("the laptop still shows the room it was taken out of")
	}
	ui.mustSay(t, "entered another room elsewhere")
}

// -- a channel's items ----------------------------------------------------------

func seqs(messages []client.Message) []int64 {
	found := make([]int64, 0, len(messages))
	for _, message := range messages {
		found = append(found, message.Seq)
	}
	return found
}

// A channel reads item by item: Enter opens the one under the cursor, and an
// opened item leaves the pending stack with the cursor following it.
func TestEnterOpensTheSelectedItemAndItLeavesThePendingStack(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	founded, err := demo.CreateChannel("News", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Subscribe(founded.ID); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"First", "Second"} {
		if err := demo.Publish(founded.ID, subject, "about "+subject); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both items", func() bool { return len(alice.Log(founded.ID)) == 2 })
	ui := headless(alice)
	ui.selectSpace(founded.ID)
	space, _ := alice.Space(founded.ID)

	// Newest first, so the cursor starts on the second.
	ui.compose("")
	pending, opened := ui.items(space)
	if !alice.Opened(founded.ID)[2] || ui.expanded != 2 ||
		!slices.Equal(seqs(pending), []int64{1}) || !slices.Equal(seqs(opened), []int64{2}) {
		t.Fatalf("opened %v, expanded %d, pending %v, opened %v",
			alice.Opened(founded.ID), ui.expanded, seqs(pending), seqs(opened))
	}
	if ui.item != 1 {
		t.Fatalf("the cursor stayed at %d rather than following the item", ui.item)
	}

	ui.move(-1)
	ui.compose("")
	if !alice.Opened(founded.ID)[1] || ui.expanded != 1 {
		t.Fatalf("the first item was not opened: %v, expanded %d", alice.Opened(founded.ID), ui.expanded)
	}
	ui.compose("")
	if ui.expanded != 0 {
		t.Fatalf("Enter again left item %d expanded", ui.expanded)
	}
}

func TestTheComposerTakesASubjectBeforeABar(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	founded, err := demo.CreateChannel("Bulletin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Subscribe(founded.ID); err != nil {
		t.Fatal(err)
	}
	ui := headless(demo)
	ui.selectSpace(founded.ID)
	ui.compose("Deploy | at four | sharp")
	ui.compose("Just a line")

	waitFor(t, "both items", func() bool { return len(demo.Log(founded.ID)) == 2 })
	var got [][2]string
	for _, message := range demo.Log(founded.ID) {
		got = append(got, [2]string{subjectOf(message), message.Body})
	}
	want := [][2]string{{"Deploy", "at four | sharp"}, {"Just a line", "Just a line"}}
	if !slices.Equal(got, want) {
		t.Fatalf("published %q, want %q", got, want)
	}
}

// system is a log: Enter on nothing typed does nothing there, and is not refused.
func TestSystemIsReadAsALog(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := headless(demo)
	ui.selectSpace(systemChannel)
	ui.compose("")
	if notices := ui.recentNotices(noticeKeep); len(notices) > 1 {
		t.Fatalf("Enter in system said %q", notices)
	}
}

// -- archival -------------------------------------------------------------------

func TestAPeriodIsANumberWithAUnit(t *testing.T) {
	for _, case_ := range []struct {
		text    string
		seconds float64
		shown   string
	}{
		{"45", 45, "45s"}, {"90s", 90, "90s"}, {"30m", 1800, "30m"}, {"1.5h", 5400, "90m"},
		{"12h", 43200, "12h"}, {"30d", 2592000, "30d"}, {"2w", 1209600, "2w"},
	} {
		seconds, err := parsePeriod(case_.text)
		if err != nil || seconds != case_.seconds || formatPeriod(seconds) != case_.shown {
			t.Errorf("%q read as %v (%v), shown as %q", case_.text, seconds, err, formatPeriod(seconds))
		}
	}
	for _, bad := range []string{"", "0", "-1d", "soon", "d", "NaN", "Inf", "1e400"} {
		if seconds, err := parsePeriod(bad); err == nil {
			t.Errorf("%q was accepted as %v", bad, seconds)
		}
	}
}

func TestArchiveShowsAndSetsTheSpacesSetting(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	founded, err := demo.CreateChannel("Notices", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client.Client{demo, alice} {
		if _, err := c.Subscribe(founded.ID); err != nil {
			t.Fatal(err)
		}
	}
	ui := headless(demo)
	ui.selectSpace(founded.ID)

	ui.command("/archive")
	ui.mustSay(t, "keeps everything")
	ui.command("/archive 30d searchable")
	ui.mustSay(t, "archives after 30d; the archive is searchable")
	if space, _ := demo.Space(founded.ID); space.Archive.Period == nil ||
		*space.Archive.Period != 2592000 || !space.Archive.Searchable {
		t.Fatalf("the channel's setting is %+v", space.Archive)
	}
	// A setting not named is kept.
	ui.command("/archive never")
	if space, _ := demo.Space(founded.ID); space.Archive.Period != nil || !space.Archive.Searchable {
		t.Fatalf("the channel's setting is %+v", space.Archive)
	}
	ui.command("/archive soon")
	ui.mustSay(t, "a period like 30d")

	other := headless(alice)
	other.selectSpace(founded.ID)
	other.command("/archive 1d")
	other.mustSay(t, "administrator")
}

// Against a server that sweeps ten times a second, so a message archives at once.
func TestArchivedAndSearchShowTheArchive(t *testing.T) {
	t.Setenv("MINOS_ROOM_SWEEP", "0.1")
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	founded, err := demo.CreateChannel("Minutes", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client.Client{demo, alice} {
		if _, err := c.Subscribe(founded.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, subject := range []string{"Budget", "Hiring"} {
		if err := demo.Publish(founded.ID, subject, "about "+subject); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both items", func() bool { return len(alice.Log(founded.ID)) == 2 })

	ui := headless(demo)
	ui.selectSpace(founded.ID)
	ui.command("/archive 0.2s")
	// The archived push is what empties alice's copy.
	waitFor(t, "the archival", func() bool { return len(alice.Log(founded.ID)) == 0 })

	ui.command("/archived")
	if len(ui.results) != 2 || !strings.Contains(ui.results[0], "Budget") ||
		!strings.Contains(ui.results[1], "Hiring") {
		t.Fatalf("the archive reads %q", ui.results)
	}

	reader := headless(alice)
	reader.selectSpace(founded.ID)
	reader.command("/search hiring")
	reader.mustSay(t, "not searchable")
	ui.command("/archive searchable")
	reader.command("/search hiring")
	if len(reader.results) != 1 || !strings.Contains(reader.results[0], "Hiring") {
		t.Fatalf("the search found %q", reader.results)
	}
}

// A user with no rooms is told how to raise one, and a user with one is not.
func TestANewUserIsToldHowToStartARoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo := connect(t, server.Base, "demo")
	headless(demo).mustSay(t, "Tab to a person")

	if _, err := demo.OpenRoom([]client.Principal{{Kind: "user", ID: "alice"}}, "", "persisted"); err != nil {
		t.Fatal(err)
	}
	alice := connect(t, server.Base, "alice")
	if notice, said := headless(alice).said("Tab to a person"); said {
		t.Fatalf("alice has a room and was still told %q", notice)
	}
}
