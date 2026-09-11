package conformance

// Archival: docs/wire-contract.md section 11. A space with a period loses its
// oldest messages to the archive as they age, without renumbering. The archive is
// the administrator's to walk and, where enabled, the audience's to search.

import (
	"fmt"
	"testing"
)

const (
	adminOnly     = "Only an administrator may do that"
	notArchivable = "Only a permanent room or a channel is archived"
	badPeriod     = "A period is a positive number of seconds"
	notSearchable = "That archive is not searchable"
)

// sweeping is a server of the test's own that sweeps five times a second, so a
// half-second period is archived within a second.
func sweeping(t *testing.T) *Server {
	t.Helper()
	return freshServer(t, map[string]string{"MINOS_ROOM_SWEEP": "0.2"})
}

func archivedThrough(room any, seq int) Pred {
	return func(e Obj) bool {
		return e["type"] == "archived" && e["room"] == room && encode(e["through"]) == fmt.Sprint(seq)
	}
}

// -- the setting ----------------------------------------------------------------

func TestEverySpaceCarriesItsArchiveSetting(t *testing.T) {
	demo := connect(t, admin)
	never := Obj{"period": nil, "searchable": false}

	same(t, demo.Call("open", "invite", []string{}, "title", unique("Room"))["archive"], never)
	same(t, demo.Call("create", "title", unique("Permanent"), "invite", []string{})["archive"], never)
	same(t, demo.Call("channel.create", "title", unique("Feed"), "groups", []string{})["archive"], never)
}

func TestOnlyAnAdministratorArchivesAndOnlyWhereArchivalApplies(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	channel := feed(demo, alice)
	// Raised with open, so user-founded even though an administrator raised it.
	adHoc := demo.Call("open", "invite", []string{}, "title", unique("Room"))

	same(t, alice.Refuse("archive.set", "room", channel, "period", 60), adminOnly)
	same(t, demo.Refuse("archive.set", "room", adHoc["id"], "period", 60), notArchivable)
	for _, period := range []any{0, -1, "soon"} {
		same(t, demo.Refuse("archive.set", "room", channel, "period", period), badPeriod)
	}
	permanent := demo.Call("create", "title", unique("Permanent"), "invite", []string{})
	demo.Call("archive.set", "room", permanent["id"], "period", 60)
}

func TestSettingArchivalIsAnnouncedAndAnAbsentFieldIsKept(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	channel := feed(demo, bob)

	reply := demo.Call("archive.set", "room", channel, "period", 3600)
	same(t, reply["ok"], true)
	same(t, obj(reply["room"])["archive"], Obj{"period": 3600, "searchable": false})
	bob.ExpectPush(func(e Obj) bool {
		pushed := obj(e["room"])
		return e["type"] == "room" && pushed["id"] == channel &&
			encode(pushed["archive"]) == `{"period":3600,"searchable":false}`
	})

	same(t, obj(demo.Call("archive.set", "room", channel, "searchable", true)["room"])["archive"],
		Obj{"period": 3600, "searchable": true})
	// Null is a value, not an absence: it means never.
	same(t, obj(demo.Call("archive.set", "room", channel, "period", nil)["room"])["archive"],
		Obj{"period": nil, "searchable": true})
}

// -- the sweep ------------------------------------------------------------------

// The oldest go first and nothing is renumbered, so every client's cursor stays true.
func TestAgedMessagesAreArchivedFromTheOldestWithoutRenumbering(t *testing.T) {
	server := sweeping(t)
	demo, alice := attach(t, server, admin), attach(t, server, "alice")
	channel := feed(demo, demo, alice)
	demo.Call("channel.publish", "channel", channel, "body", "one")
	demo.Call("channel.publish", "channel", channel, "body", "two")
	alice.Call("channel.open", "channel", channel, "seq", 1)

	// Set after publishing: a new period applies to what is already there.
	demo.Call("archive.set", "room", channel, "period", 0.5)
	alice.ExpectPush(archivedThrough(channel, 2))

	// Opened or not, both went.
	reply := alice.Call("history", "room", channel, "since", 0)
	same(t, []any{reply["messages"], reply["lastSeq"]}, []any{[]any{}, 2})
	same(t, demo.Call("channel.publish", "channel", channel, "body", "three")["seq"], 3)
}

func TestASpaceWithNoPeriodKeepsEverything(t *testing.T) {
	server := sweeping(t)
	demo := attach(t, server, admin)
	kept, aged := feed(demo, demo), feed(demo, demo)
	demo.Call("channel.publish", "channel", kept, "body", "kept")
	demo.Call("channel.publish", "channel", aged, "body", "aged")

	demo.Call("archive.set", "room", aged, "period", 0.5)
	demo.ExpectPush(archivedThrough(aged, 1))
	same(t, pluck(demo.Call("history", "room", kept, "since", 0)["messages"], "body"), []string{"kept"})
}

// -- reading it -----------------------------------------------------------------

func TestTheAdministratorPagesThroughTheArchive(t *testing.T) {
	server := sweeping(t)
	demo, alice := attach(t, server, admin), attach(t, server, "alice")
	channel := feed(demo, demo, alice)
	for n := 1; n <= 201; n++ {
		demo.Call("channel.publish", "channel", channel, "body", fmt.Sprint("m", n))
	}
	demo.Call("archive.set", "room", channel, "period", 0.5)
	demo.ExpectPush(archivedThrough(channel, 201))

	page := demo.Call("archive.read", "room", channel)
	messages := list(page["messages"])
	truth(t, len(messages) == 200, "the first page holds %d", len(messages))
	same(t, []any{obj(messages[0])["seq"], obj(messages[199])["seq"], page["after"], page["more"]},
		[]any{1, 200, 0, true})
	// Archived unchanged: the message shape, subject included.
	keySet(t, obj(messages[0]), "room", "seq", "author", "kind", "subject", "body", "at")

	last := demo.Call("archive.read", "room", channel, "after", 200)
	same(t, []any{pluck(last["messages"], "body"), last["more"]}, []any{[]string{"m201"}, false})
	same(t, alice.Refuse("archive.read", "room", channel), adminOnly)
}

func TestSearchIsTheAdministratorsUntilItIsEnabled(t *testing.T) {
	server := sweeping(t)
	demo, alice, bob := attach(t, server, admin), attach(t, server, "alice"), attach(t, server, "bob")
	channel := feed(demo, demo, alice)
	demo.Call("channel.publish", "channel", channel, "subject", "Deploy at four", "body", "all hands")
	demo.Call("channel.publish", "channel", channel, "subject", "Lunch", "body", "deploy the sandwiches")
	demo.Call("archive.set", "room", channel, "period", 0.5)
	alice.ExpectPush(archivedThrough(channel, 2))

	same(t, alice.Refuse("archive.search", "room", channel, "query", "deploy"), notSearchable)
	// Subject or body, without case, newest first.
	same(t, pluck(demo.Call("archive.search", "room", channel, "query", "  DEPLOY ")["messages"], "seq"),
		[]int{2, 1})

	demo.Call("archive.set", "room", channel, "searchable", true)
	same(t, pluck(alice.Call("archive.search", "room", channel, "query", "sandwich")["messages"], "seq"),
		[]int{2})
	same(t, alice.Refuse("archive.search", "room", channel, "query", "  "), "Empty query")
	same(t, bob.Refuse("archive.search", "room", channel, "query", "deploy"),
		bob.Refuse("history", "room", channel, "since", 0))
}

// The live messages are already in the channel; search is the path to what left it.
func TestSearchReachesOnlyTheArchive(t *testing.T) {
	server := sweeping(t)
	demo := attach(t, server, admin)
	channel := feed(demo, demo)
	demo.Call("channel.publish", "channel", channel, "body", "old news")
	demo.Call("archive.set", "room", channel, "period", 0.5, "searchable", true)
	demo.ExpectPush(archivedThrough(channel, 1))
	demo.Call("archive.set", "room", channel, "period", nil)
	demo.Call("channel.publish", "channel", channel, "body", "new news")

	same(t, pluck(demo.Call("archive.search", "room", channel, "query", "news")["messages"], "body"),
		[]string{"old news"})
}

func TestSearchReturnsAtMostAHundredNewestFirst(t *testing.T) {
	server := sweeping(t)
	demo := attach(t, server, admin)
	channel := feed(demo, demo)
	for n := 1; n <= 101; n++ {
		demo.Call("channel.publish", "channel", channel, "body", fmt.Sprint("match ", n))
	}
	demo.Call("archive.set", "room", channel, "period", 0.5)
	demo.ExpectPush(archivedThrough(channel, 101))

	seqs := pluck(demo.Call("archive.search", "room", channel, "query", "match")["messages"], "seq")
	truth(t, len(seqs) == 100, "search returned %d", len(seqs))
	same(t, []any{seqs[0], seqs[99]}, []any{101, 2})
}

func TestAPermanentRoomIsArchivedAndSearchedByItsParticipants(t *testing.T) {
	server := sweeping(t)
	demo, alice := attach(t, server, admin), attach(t, server, "alice")
	room := demo.Call("create", "title", unique("Permanent"), "invite", []string{})
	demo.Call("invite", "room", room["id"], "principal", "alice")
	seq := int(num(alice.Call("send", "room", room["id"], "body", "minutes of the meeting")["seq"]))

	demo.Call("archive.set", "room", room["id"], "period", 0.5, "searchable", true)
	alice.ExpectPush(archivedThrough(room["id"], seq))
	same(t, pluck(alice.Call("archive.search", "room", room["id"], "query", "minutes")["messages"], "seq"),
		[]int{seq})
}
