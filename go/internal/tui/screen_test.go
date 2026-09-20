package tui

// Drawing and keys, on tcell's simulated screen.

import (
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

func TestWrapFillsLinesAsTextwrapDoes(t *testing.T) {
	for _, case_ := range []struct {
		text  string
		width int
		want  []string
	}{
		{"hello world", 5, []string{"hello", "world"}},
		{"hello world", 11, []string{"hello world"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"ab cdefghij", 5, []string{"ab cd", "efghi", "j"}},
		{"a\tb\nc", 10, []string{"a b c"}},
		{"", 10, nil},
		{"   ", 10, nil},
		// Two cells each: a line of five cells holds two.
		{"日本語です", 5, []string{"日本", "語で", "す"}},
		{"ab 日本語", 5, []string{"ab 日", "本語"}},
	} {
		if got := wrap(case_.text, case_.width); !slices.Equal(got, case_.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", case_.text, case_.width, got, case_.want)
		}
	}
}

// A wide character takes two cells, so a field of five holds two of them and
// nothing spills past it. A bidi override is shown, not obeyed.
func TestPutMeasuresCellsAndShowsControls(t *testing.T) {
	screen := simulated(t, 8, 1)
	ui := &Ui{screen: screen}
	ui.put(0, 0, "日本語", 5, plain, false)
	ui.put(0, 5, "x\u202ey", 3, plain, false)
	screen.Show()
	if got := contents(screen); got != "日 本  x?y" {
		t.Fatalf("drew %q", got)
	}
}

func simulated(t *testing.T, width, height int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.SetSize(width, height)
	return screen
}

// rowFields is the whitespace-separated fields of the drawn line holding text,
// so a test names what a row says without pinning how wide its columns are.
func rowFields(drawn, text string) []string {
	for _, line := range strings.Split(drawn, "\n") {
		if strings.Contains(line, text) {
			return strings.Fields(line)
		}
	}
	return nil
}

func contents(screen tcell.SimulationScreen) string {
	cells, width, _ := screen.GetContents()
	var text strings.Builder
	for index, cell := range cells {
		if index > 0 && index%width == 0 {
			text.WriteByte('\n')
		}
		if len(cell.Runes) > 0 {
			text.WriteRune(cell.Runes[0])
		} else {
			text.WriteByte(' ')
		}
	}
	return text.String()
}

func TestTheScreenIsDrawnAndATinyOneIsRefused(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	ui.command("/open alice")
	room := ui.selected
	if err := demo.Send(room, strings.Repeat("word ", 30)+"\x1b[2J"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(demo.Log(room)) == 1 })

	ui.ensureSelection()
	ui.draw()
	drawn := contents(screen)
	// In the room, the screen is that room.
	for _, want := range []string{" demo (admin)  in alice, demo", "connected", "word word", "> ", "/exit"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the room lacks %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "PROJECTS") {
		t.Errorf("the room shows the tab bar:\n%s", drawn)
	}
	if strings.Contains(drawn, "disconnected") || strings.ContainsRune(drawn, '\x1b') {
		t.Errorf("the screen is wrong:\n%q", drawn)
	}
	// Drawn with the newest message on screen, so it counts as seen.
	if demo.Unread(room) != 0 {
		t.Errorf("unread %d after drawing", demo.Unread(room))
	}

	// Stepping out brings the tab bar and the rooms list back, with the room
	// still highlighted.
	ui.command("/exit")
	ui.draw()
	for _, want := range []string{"PROJECTS", "ROOMS", "PEOPLE", "PLACE", "Enter: go in"} {
		if drawn := contents(screen); !strings.Contains(drawn, want) {
			t.Errorf("stepping out lacks %q:\n%s", want, drawn)
		}
	}
	if ui.selected != room {
		t.Errorf("stepping out lost the room: selected %q", ui.selected)
	}

	// A highlighted room is a preview: drawing it marks nothing read.
	if err := demo.Send(room, "later"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second message", func() bool { return len(demo.Log(room)) == 2 })
	ui.draw()
	if unread := demo.Unread(room); unread != 1 {
		t.Errorf("the preview marked the room read: unread %d", unread)
	}

	screen.SetSize(30, 5)
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "Terminal too small") {
		t.Errorf("a tiny screen shows:\n%s", drawn)
	}
}

func TestTypingAndEnterSendFromTheComposer(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	ui := newUi(simulated(t, 80, 24), demo, client.Profile{Username: "demo"})
	ui.command("/open alice")
	room := ui.selected

	for _, r := range "hellp" {
		ui.key(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	ui.key(tcell.NewEventKey(tcell.KeyBackspace2, 0, tcell.ModNone))
	ui.key(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone))
	ui.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	waitFor(t, "the message", func() bool { return len(demo.Log(room)) == 1 })
	if body := demo.Log(room)[0].Body; body != "hello" {
		t.Fatalf("sent %q", body)
	}

	ui.key(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	if ui.running {
		t.Fatal("^C did not stop the loop")
	}
}

func TestAChannelShowsSubjectsAndTheOpenedBody(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	founded, err := demo.CreateChannel("News", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Subscribe(founded.ID); err != nil {
		t.Fatal(err)
	}
	if err := demo.Publish(founded.ID, "Deploy at four", "all hands, room 2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the item", func() bool { return len(alice.Log(founded.ID)) == 1 })

	screen := simulated(t, 80, 24)
	ui := newUi(screen, alice, client.Profile{Username: "alice"})
	ui.show(founded.ID)
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"Pending (1)", "Deploy at four", "Up/Down: item"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the channel lacks %q:\n%s", want, drawn)
		}
	}
	// Collapsed until asked for, and counted nowhere outside the channel.
	if strings.Contains(drawn, "all hands") || strings.Contains(drawn, "News (") {
		t.Errorf("the channel is drawn wrong:\n%s", drawn)
	}

	ui.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	ui.draw()
	drawn = contents(screen)
	for _, want := range []string{"all hands, room 2", "Pending (0)", "Opened"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the opened item lacks %q:\n%s", want, drawn)
		}
	}
}

// The status line counts open invitations and unread messages apart, and the
// sidebar marks a room not yet entered.
func TestInvitationsAndUnreadMessagesAreShownApart(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room, err := demo.OpenRoom([]client.Principal{{Kind: "user", ID: "alice"}}, "Standup", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	if err := demo.Send(room.ID, "one"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the invitation", func() bool { return len(alice.Log(room.ID)) == 1 })

	screen := simulated(t, 100, 24)
	ui := newUi(screen, alice, client.Profile{Username: "alice"})
	ui.showTabRooms(room.ID)
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"1 open invitation", "Standup", "invited"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("before entering, the screen lacks %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "unread message") {
		t.Errorf("an invitation's messages were counted unread:\n%s", drawn)
	}

	// Entered and read, then out again with something new: unread, and no longer invited.
	ui.compose("")
	ui.draw()
	ui.command("/exit")
	ui.draw()
	if err := demo.Send(room.ID, "two"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second message", func() bool { return len(alice.Log(room.ID)) == 2 })
	ui.draw()
	drawn = contents(screen)
	// The header's "2 invited" is the audience, not an invitation, so the check
	// names the marker and the count exactly.
	if !strings.Contains(drawn, "1 unread message") ||
		strings.Contains(drawn, "invited") || strings.Contains(drawn, "open invitation") {
		t.Errorf("after the visit the screen is wrong:\n%s", drawn)
	}
}

func TestArchiveResultsReplaceThePaneUntilEsc(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	ui.show(systemChannel)
	ui.showResults("System: 1 archived match(es) for 'budget'",
		[]string{"[1] 2026-09-11 10:00 demo       Budget"})

	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"archived match(es)", "Esc: back to the conversation", "Budget", " Esc: back"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the results lack %q:\n%s", want, drawn)
		}
	}

	// Esc closes the results first, leaving the channel that was under them.
	ui.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); strings.Contains(drawn, "archived match(es)") ||
		!strings.Contains(drawn, "System") {
		t.Errorf("Esc left the results up:\n%s", drawn)
	}

	// A second Esc steps out of the channel, to the list it was listed in.
	ui.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "PLACE") {
		t.Errorf("Esc did not leave the channel:\n%s", drawn)
	}
}

// What Enter does to a person, and how to invite in a room, are on the screen.
func TestAPersonAndARoomSayWhatEnterAndInviteDo(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})

	ui.showTab(tabPeople)
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"PERSON", "> alice", "(to alice)", "Enter: open a room with alice"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the people list lacks %q:\n%s", want, drawn)
		}
	}

	ui.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "/invite <who>") {
		t.Errorf("a room does not say how to invite:\n%s", drawn)
	}
}

// The projects tab is a table of containers, and Enter on a row is the rooms
// it holds. Counts are per project, because a project holds no messages of its
// own.
func TestTheProjectsTabTabulatesWhatEachProjectHolds(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room, err := demo.CreateRoom("Standup", []client.Principal{{Kind: "user", ID: "alice"}}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	news, err := demo.CreateChannel("News", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// A founder is not in a channel's audience until they subscribe, and the
	// list shows what the user reaches.
	if _, err := demo.Subscribe(news.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the channel", func() bool { _, ok := demo.Space(news.ID); return ok })

	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	for _, args := range [][]string{
		{"new", "cynn"}, {"file", "Standup", "cynn"}, {"file", "News", "cynn"},
	} {
		if err := ui.cmdProject(args); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "alice to be told", func() bool { _, ok := alice.Space(room.ID); return ok })

	ui.showTab(tabProjects)
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"PROJECT", "ROOMS", "CHANNELS", "LAST", "> cynn", "(none)"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the projects tab lacks %q:\n%s", want, drawn)
		}
	}

	// In: only that project's places, and the room says which project it is in.
	ui.compose("")
	if rows := ui.roomRows(); len(rows) != 2 {
		t.Fatalf("the project holds %d places, want 2", len(rows))
	}
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "Standup") ||
		!strings.Contains(drawn, "News") || strings.Contains(drawn, "System") {
		t.Errorf("the scoped list is wrong:\n%s", drawn)
	}

	// In the place itself the tab bar is gone, so its header carries the
	// qualified name: which project, then which place in it.
	ui.show(news.ID)
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "cynn/News") {
		t.Errorf("the channel header does not qualify its name:\n%s", drawn)
	}
}

// The header bar carries the name and the tabs, as gwiki's does, and the
// overview opens on where the work is.
func TestTheHeaderBarCarriesTheNameAndTheOverviewOpensFirst(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room, err := demo.CreateRoom("task/31", []client.Principal{{Kind: "user", ID: "alice"}}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.Send(room.ID, "starting on it"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(demo.Log(room.ID)) == 1 })

	screen := simulated(t, 86, 20)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	for _, args := range [][]string{{"new", "cynn", "#go"}, {"file", "task/31", "cynn", "31"}} {
		if err := ui.cmdProject(args); err != nil {
			t.Fatal(err)
		}
	}
	ui.draw()

	drawn := contents(screen)
	for _, want := range []string{
		" minos   OVERVIEW   PROJECTS   ROOMS   PEOPLE",
		"PROJECT", "TAGS", "ACTIVE", "SAID", "BUSIEST",
		"> cynn", "go", "task/31",
		"1 project", "places open",
	} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the overview lacks %q:\n%s", want, drawn)
		}
	}

	// Enter opens that project's own page: what it holds, then its places.
	ui.compose("")
	ui.draw()
	drawn = contents(screen)
	for _, want := range []string{"cynn  #go", "1 open of 1 place", "SCOPE", "TASK"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the project page lacks %q:\n%s", want, drawn)
		}
	}
	// The row's own fields, rather than the spacing between them.
	if fields := rowFields(drawn, "task/31"); !slices.Equal(fields,
		[]string{">", "task/31", "room", "task", "31", "1"}) {
		t.Errorf("the task room's row is %q:\n%s", fields, drawn)
	}

	// Closed places are out of the way until asked for.
	if err := ui.cmdClose([]string{"task/31"}); err != nil {
		t.Fatal(err)
	}
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "0 open of 1 place, 1 closed") ||
		!strings.Contains(drawn, "a shows the closed ones") {
		t.Errorf("a closed place is drawn wrong:\n%s", drawn)
	}
	ui.key(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "closed") ||
		!strings.Contains(drawn, "task/31") {
		t.Errorf("a did not bring the closed place back:\n%s", drawn)
	}
}
