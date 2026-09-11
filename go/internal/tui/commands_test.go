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
