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
	} {
		if got := wrap(case_.text, case_.width); !slices.Equal(got, case_.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", case_.text, case_.width, got, case_.want)
		}
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
	for _, want := range []string{" minos  demo (admin)  in alice, demo", "connected", "word word", "> ", "/exit"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the room lacks %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "CHANNELS") {
		t.Errorf("the room shows the sidebar:\n%s", drawn)
	}
	if strings.Contains(drawn, "disconnected") || strings.ContainsRune(drawn, '\x1b') {
		t.Errorf("the screen is wrong:\n%q", drawn)
	}
	// Drawn with the newest message on screen, so it counts as seen.
	if demo.Unread(room) != 0 {
		t.Errorf("unread %d after drawing", demo.Unread(room))
	}

	// Stepping out brings the rest back, with the room still highlighted.
	ui.command("/exit")
	ui.draw()
	for _, want := range []string{"ROOMS", "CHANNELS", "PEOPLE", "Enter to join"} {
		if drawn := contents(screen); !strings.Contains(drawn, want) {
			t.Errorf("stepping out lacks %q:\n%s", want, drawn)
		}
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
	founded, err := demo.CreateChannel("News", nil)
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
	ui.selectSpace(founded.ID)
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

func TestArchiveResultsReplaceThePaneUntilEsc(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	ui.ensureSelection()
	ui.showResults("System: 1 archived match(es) for 'budget'",
		[]string{"[1] 2026-09-11 10:00 demo       Budget"})

	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"archived match(es)", "Esc: back to the conversation", "Budget", " Esc: back"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the results lack %q:\n%s", want, drawn)
		}
	}

	ui.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); strings.Contains(drawn, "Esc: back") || !strings.Contains(drawn, "System") {
		t.Errorf("Esc left the results up:\n%s", drawn)
	}
}

// What Enter does to a person, and how to invite in a room, are on the screen.
func TestAPersonAndARoomSayWhatEnterAndInviteDo(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})

	ui.selectPerson("alice")
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{">  alice", "(to alice)", "Enter: open a room with alice", "Enter raises a room"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("a selected person lacks %q:\n%s", want, drawn)
		}
	}

	ui.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	ui.draw()
	if drawn := contents(screen); !strings.Contains(drawn, "/invite <who>") {
		t.Errorf("a room does not say how to invite:\n%s", drawn)
	}
}
