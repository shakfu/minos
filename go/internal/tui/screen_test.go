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
	for _, want := range []string{" minos  demo (admin)", "connected", "ROOMS", "CHANNELS", "PEOPLE", "word word", "> "} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the screen lacks %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "disconnected") || strings.ContainsRune(drawn, '\x1b') {
		t.Errorf("the screen is wrong:\n%q", drawn)
	}
	// Drawn with the newest message on screen, so it counts as seen.
	if demo.Unread(room) != 0 {
		t.Errorf("unread %d after drawing", demo.Unread(room))
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
