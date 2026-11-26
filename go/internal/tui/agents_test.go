package tui

// What a room shows of its agents: author marks, the run's state and what it
// has been handed, folded progress, and decision cards decided in place.

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

// runRoom is a room demo raises for worker and alice, as a dispatcher would.
func runRoom(t *testing.T) (demo, alice, worker *client.Client, room string) {
	t.Helper()
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice, worker = connect(t, server.Base, "demo"), connect(t, server.Base, "alice"), connect(t, server.Base, "worker")
	opened, err := demo.OpenRoom([]client.Principal{{Kind: "user", ID: "worker"}, {Kind: "user", ID: "alice"}},
		"Run", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*client.Client{alice, worker} {
		waitFor(t, "the invitation", func() bool { _, ok := c.Space(opened.ID); return ok })
	}
	return demo, alice, worker, opened.ID
}

func drawnBy(t *testing.T, c *client.Client, room string) (*Ui, func() string) {
	t.Helper()
	screen := simulated(t, 100, 30)
	ui := newUi(screen, c, client.Profile{Username: c.Me()})
	ui.show(room)
	return ui, func() string {
		ui.draw()
		return contents(screen)
	}
}

func mustShow(t *testing.T, drawn string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(drawn, want) {
			t.Errorf("the screen lacks %q:\n%s", want, drawn)
		}
	}
}

func TestEachMessageIsMarkedWithItsAuthorsKind(t *testing.T) {
	demo, alice, worker, room := runRoom(t)
	say := func(c *client.Client, body string) {
		if err := c.Send(room, body); err != nil {
			t.Fatal(err)
		}
	}
	say(alice, "please fix it")
	say(worker, "on it")
	if _, err := demo.SetState(room, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "three messages", func() bool { return len(alice.Log(room)) == 3 })

	_, draw := drawnBy(t, alice, room)
	mustShow(t, draw(), markPerson+" alice", markAgent+" worker", markSystem+" system")
}

func TestTheRoomShowsWhatTheRunIsDoingAndWhatItHasNotBeenHanded(t *testing.T) {
	_, alice, worker, room := runRoom(t)
	if _, err := worker.Enter(room); err != nil {
		t.Fatal(err)
	}
	if err := alice.Send(room, "handed over"); err != nil {
		t.Fatal(err)
	}
	if err := worker.ReportAgent(room, client.AgentReport{State: "working", Turn: 2, Through: 1}); err != nil {
		t.Fatal(err)
	}
	if err := alice.Send(room, "and the docs"); err != nil {
		t.Fatal(err)
	}
	if err := worker.ReportAgent(room, client.AgentReport{State: "working", Turn: 2, Held: 1, Through: 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the held report", func() bool {
		states := alice.Agents(room)
		return len(states) == 1 && states[0].Held == 1 && len(alice.Log(room)) == 2
	})

	ui, draw := drawnBy(t, alice, room)
	drawn := draw()
	mustShow(t, drawn, markAgent+" worker  working · turn 2", "1 message held for the next turn",
		"held: worker gets it when this turn ends", "^X: interrupt")
	if strings.Contains(drawn, "has it") {
		t.Errorf("a handed-over message that is not the newest says so:\n%s", drawn)
	}

	ui.key(tcell.NewEventKey(tcell.KeyCtrlX, 0, tcell.ModNone))
	if string(ui.input) != "/interrupt " {
		t.Fatalf("^X typed %q", string(ui.input))
	}

	// The rooms list says it too.
	ui.showTab(tabRooms)
	mustShow(t, draw(), markAgent+" working")
}

func TestARunOfProgressFoldsUntilAskedFor(t *testing.T) {
	_, alice, worker, room := runRoom(t)
	for _, step := range []string{"Read go.mod", "Edit x.go", "Bash go test"} {
		if err := worker.SendKind(room, step, "progress"); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the progress", func() bool { return len(alice.Log(room)) == 3 })

	ui, draw := drawnBy(t, alice, room)
	drawn := draw()
	mustShow(t, drawn, "◆ worker     ▸ 3 progress lines", "latest: Bash go test")
	if strings.Contains(drawn, "Edit x.go") {
		t.Errorf("folded progress shows its lines:\n%s", drawn)
	}

	ui.key(tcell.NewEventKey(tcell.KeyCtrlE, 0, tcell.ModNone))
	mustShow(t, draw(), "· Read go.mod", "· Edit x.go", "· Bash go test")
}

// decisionRoom is runRoom with a channel alice moderates and worker submits to.
func decisionRoom(t *testing.T) (alice, worker *client.Client, room, channel string) {
	t.Helper()
	demo, alice, worker, room := runRoom(t)
	created, err := demo.CreateChannel("Decisions", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Appoint(created.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Subscribe(created.ID); err != nil {
		t.Fatal(err)
	}
	return alice, worker, room, created.ID
}

func TestARequestIsACardInItsRoomAndIsApprovedThere(t *testing.T) {
	alice, worker, room, channel := decisionRoom(t)
	submission, err := worker.SubmitFrom(channel, "Deploy to staging?", "all tests pass", room)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice's queue", func() bool { _, ok := alice.Queued()[submission.ID]; return ok })

	ui, draw := drawnBy(t, alice, room)
	mustShow(t, draw(), "Decision needed · worker · just now", "Deploy to staging?", "all tests pass",
		"^Y: approve")

	ui.key(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModNone))
	ui.mustSay(t, "Approved worker's request")
	waitFor(t, "the room to be told", func() bool {
		log := alice.Log(room)
		return len(log) > 0 && strings.Contains(log[len(log)-1].Body, "alice approved worker's request")
	})
	if strings.Contains(draw(), "Decision needed") {
		t.Error("the decided card is still drawn")
	}
}

func TestARejectionTakesItsReasonFromTheComposer(t *testing.T) {
	alice, worker, room, channel := decisionRoom(t)
	submission, err := worker.SubmitFrom(channel, "Merge?", "", room)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice's queue", func() bool { _, ok := alice.Queued()[submission.ID]; return ok })

	ui, _ := drawnBy(t, alice, room)
	ui.input = []rune("tests fail")
	ui.key(tcell.NewEventKey(tcell.KeyCtrlR, 0, tcell.ModNone))
	ui.mustSay(t, "Rejected worker's request")
	waitFor(t, "worker to be told", func() bool {
		got := worker.Submissions()[submission.ID]
		return got.State == "rejected" && got.Comment != nil && *got.Comment == "tests fail"
	})
	if len(ui.input) != 0 {
		t.Errorf("the reason stayed in the composer: %q", string(ui.input))
	}
}

func TestTheAuthorSeesItsRequestWaitingAndCannotDecideIt(t *testing.T) {
	_, worker, room, channel := decisionRoom(t)
	if _, err := worker.SubmitFrom(channel, "Deploy?", "", room); err != nil {
		t.Fatal(err)
	}
	ui, draw := drawnBy(t, worker, room)
	mustShow(t, draw(), "Waiting for a moderator", "you asked; a moderator decides")
	ui.key(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModNone))
	ui.mustSay(t, "Nothing here waits on your decision")
}

func TestAModeratorsChannelShowsItsQueueAsCards(t *testing.T) {
	alice, worker, _, channel := decisionRoom(t)
	submission, err := worker.Submit(channel, "A tip", "read the docs")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice's queue", func() bool { _, ok := alice.Queued()[submission.ID]; return ok })
	if _, err := alice.Subscribe(channel); err != nil {
		t.Fatal(err)
	}
	ui, draw := drawnBy(t, alice, channel)
	mustShow(t, draw(), "Decision needed · worker → Decisions", "A tip")
	ui.key(tcell.NewEventKey(tcell.KeyCtrlY, 0, tcell.ModNone))
	waitFor(t, "the approval", func() bool { return len(alice.Log(channel)) == 1 })
}
