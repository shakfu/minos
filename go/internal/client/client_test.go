package client

// The protocol client against a real HTTP server and a real websocket: a mock of
// the transport would prove nothing about the thing most likely to be wrong.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"minos/internal/config"
	"minos/internal/testserver"
)

func connect(t *testing.T, base, username string) *Client {
	t.Helper()
	_, client, _, err := Connect(base, username, username)
	if err != nil {
		t.Fatalf("%s cannot connect: %v", username, err)
	}
	t.Cleanup(client.Stop)
	return client
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

func pair(t *testing.T, demo *Client) Room {
	t.Helper()
	room, err := demo.OpenRoom([]Principal{{Kind: "user", ID: "alice"}}, "Pair", "persisted")
	if err != nil {
		t.Fatalf("cannot open a room: %v", err)
	}
	return room
}

func send(t *testing.T, c *Client, room string, bodies ...string) {
	t.Helper()
	for _, body := range bodies {
		if err := c.Send(room, body); err != nil {
			t.Fatalf("cannot send %q: %v", body, err)
		}
	}
}

func bodies(messages []Message) []string {
	found := make([]string, 0, len(messages))
	for _, message := range messages {
		found = append(found, message.Body)
	}
	return found
}

// -- the connection ----------------------------------------------------------

func TestLoginAndSyncOverARealSocket(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")

	if demo.Me() != "demo" || !demo.IsAdmin() {
		t.Fatalf("synced as %q, admin %v", demo.Me(), demo.IsAdmin())
	}
	var titles []string
	for _, channel := range demo.Channels() {
		titles = append(titles, channel.Title)
	}
	if !slices.Equal(titles, []string{"System"}) {
		t.Fatalf("channels are %v", titles)
	}
	if len(demo.Rooms()) != 0 {
		t.Fatalf("rooms are %v", demo.Rooms())
	}
}

func TestAnOrdinaryUserIsNotAnAdmin(t *testing.T) {
	alice := connect(t, testserver.Start(t, config.HistoryLimit).Base, "alice")

	if alice.IsAdmin() {
		t.Fatal("alice is an administrator")
	}
	var refusal *ChatError
	if _, err := alice.CreateRoom("Engineering", nil); !errors.As(err, &refusal) {
		t.Fatalf("founding a permanent room gave %v", err)
	}
}

func TestABadPasswordIsRefusedBeforeTheScreenStarts(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)

	var refusal *TransportError
	if _, _, _, err := Connect(server.Base, "demo", "wrong"); !errors.As(err, &refusal) {
		t.Fatalf("a bad password gave %v", err)
	}
}

// -- delivery ----------------------------------------------------------------

func TestAMessageTravelsBetweenTwoTerminals(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room := pair(t, demo)

	send(t, demo, room.ID, "hello from the terminal")

	waitFor(t, "the message", func() bool { return len(alice.Log(room.ID)) > 0 })
	delivered := alice.Log(room.ID)[0]
	if delivered.Body != "hello from the terminal" || delivered.Author != "demo" {
		t.Fatalf("delivered %+v", delivered)
	}
}

// The cursor is wound back by hand to stand in for dropped pushes. What matters
// is that the client notices and asks, rather than rendering next to a hole.
func TestAGapIsRepairedRatherThanSkipped(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room := pair(t, demo)
	send(t, demo, room.ID, "one", "two", "three")
	waitFor(t, "three messages", func() bool { return len(alice.Log(room.ID)) == 3 })

	alice.mutex.Lock()
	alice.cursors[room.ID] = 1
	alice.log[room.ID] = alice.log[room.ID][:1]
	alice.mutex.Unlock()

	send(t, demo, room.ID, "four")
	waitFor(t, "the repair", func() bool { return len(alice.Log(room.ID)) == 4 })
	if got := bodies(alice.Log(room.ID)); !slices.Equal(got, []string{"one", "two", "three", "four"}) {
		t.Fatalf("the log is %v", got)
	}
}

// A backfill capped at the tail leaves a gap that will never be filled, and the
// client says how much is missing rather than closing it in silence.
func TestAShortfallIsReportedRatherThanHidden(t *testing.T) {
	server := testserver.Start(t, 2)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room := pair(t, demo)
	send(t, demo, room.ID, "0", "1", "2", "3", "4", "5")
	waitFor(t, "six messages", func() bool { return len(alice.Log(room.ID)) == 6 })

	alice.mutex.Lock()
	alice.log[room.ID] = nil
	alice.cursors[room.ID] = 0
	alice.mutex.Unlock()

	alice.repair(room.ID)
	got := bodies(alice.Log(room.ID))
	if !slices.Equal(got, []string{"4 earlier message(s) not shown", "4", "5"}) {
		t.Fatalf("the log is %v", got)
	}
}

// -- the model, through the client -------------------------------------------

func TestTwoRoomsMayHoldTheSamePeople(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	alice := []Principal{{Kind: "user", ID: "alice"}}

	first, err := demo.OpenRoom(alice, "One", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	second, err := demo.OpenRoom(alice, "Two", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || !slices.Equal(first.Audience, second.Audience) {
		t.Fatalf("rooms %+v and %+v", first, second)
	}
}

func TestAGroupCanBeInvitedAndTracksItsMembership(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")

	group, err := demo.CreateGroup("Team", nil)
	if err != nil {
		t.Fatal(err)
	}
	room, err := demo.CreateRoom("Team room", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Invite(room.ID, Principal{Kind: "group", ID: group.ID}); err != nil {
		t.Fatal(err)
	}

	history := map[string]any{"room": room.ID, "since": 0}
	if _, err := alice.request("history", history); err == nil {
		t.Fatal("alice read a room her group was not yet carrying her into")
	}

	if _, err := demo.AssignGroup(group.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	reply, err := alice.request("history", history)
	if err != nil {
		t.Fatalf("history after assignment: %v", err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(reply, &fields) != nil || fields["messages"] == nil {
		t.Fatalf("history replied %s", reply)
	}
}

func TestEnteringARoomIsNotBeingInvitedToIt(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo := connect(t, server.Base, "demo")
	room, err := demo.OpenRoom(nil, "Meeting", "persisted")
	if err != nil {
		t.Fatal(err)
	}

	occupancy, err := demo.Enter(room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := server.Store.OccupantsOf(room.ID); !slices.Equal(got, []string{"demo"}) {
		t.Fatalf("occupants are %v", got)
	}

	if err := demo.Exit(occupancy); err != nil {
		t.Fatal(err)
	}
	if got := server.Store.OccupantsOf(room.ID); len(got) != 0 {
		t.Fatalf("occupants are %v", got)
	}
	if err := demo.call("history", map[string]any{"room": room.ID, "since": 0}, nil); err != nil {
		t.Fatalf("leaving the room took access with it: %v", err)
	}
}

// Leaving is closing the program, which is what a transient room counts on.
func TestClosingTheTerminalGivesUpEveryRoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo := connect(t, server.Base, "demo")
	room, err := demo.OpenRoom(nil, "Standup", "transient")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Enter(room.ID); err != nil {
		t.Fatal(err)
	}
	if got := server.Store.OccupantsOf(room.ID); !slices.Equal(got, []string{"demo"}) {
		t.Fatalf("occupants are %v", got)
	}

	demo.Stop()
	waitFor(t, "the room to empty", func() bool { return len(server.Store.OccupantsOf(room.ID)) == 0 })
}

func TestAChannelIsReadOnly(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")

	err := demo.Send("system", "can I post here")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("sending to a channel gave %v", err)
	}
}

func TestTheReadCursorIsSeparateFromTheDeliveryCursor(t *testing.T) {
	demo := connect(t, testserver.Start(t, config.HistoryLimit).Base, "demo")
	room, err := demo.OpenRoom(nil, "Notes", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	send(t, demo, room.ID, "one", "two")

	// Received, and so applied to the delivery cursor.
	waitFor(t, "the delivery cursor", func() bool {
		demo.mutex.Lock()
		defer demo.mutex.Unlock()
		return demo.cursors[room.ID] == 2
	})
	// But not yet seen: nothing marked it read.
	if demo.ReadCursor(room.ID) != 0 || demo.Unread(room.ID) != 2 {
		t.Fatalf("read %d, unread %d", demo.ReadCursor(room.ID), demo.Unread(room.ID))
	}

	if err := demo.MarkRead(room.ID, 2); err != nil {
		t.Fatal(err)
	}
	if demo.Unread(room.ID) != 0 {
		t.Fatalf("unread %d after marking", demo.Unread(room.ID))
	}
}

// Open invitations and unread messages are counted apart, and channels in neither.
func TestInvitationsAndUnreadMessagesAreCountedApart(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	room := pair(t, demo)
	send(t, demo, room.ID, "one", "two")
	waitFor(t, "the invitation", func() bool { return len(alice.Log(room.ID)) == 2 })

	if alice.OpenInvitations() != 1 || alice.UnreadMessages() != 0 {
		t.Fatalf("before entering: %d invitations, %d unread", alice.OpenInvitations(), alice.UnreadMessages())
	}
	if _, err := alice.Enter(room.ID); err != nil {
		t.Fatal(err)
	}
	if alice.OpenInvitations() != 0 || alice.UnreadMessages() != 2 {
		t.Fatalf("after entering: %d invitations, %d unread", alice.OpenInvitations(), alice.UnreadMessages())
	}

	// A room of her own is not an invitation, and a channel's items are not counted.
	if _, err := alice.OpenRoom(nil, "Mine", "persisted"); err != nil {
		t.Fatal(err)
	}
	feed := channel(t, demo, alice)
	if err := demo.Publish(feed, "", "news"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the channel item", func() bool { return len(alice.Log(feed)) == 1 })
	if alice.OpenInvitations() != 0 || alice.UnreadMessages() != 2 {
		t.Fatalf("with a room and a channel: %d invitations, %d unread", alice.OpenInvitations(), alice.UnreadMessages())
	}

	// The visit is the server's, so another device knows it from the start.
	if later := connect(t, server.Base, "alice"); later.OpenInvitations() != 0 || later.UnreadMessages() != 2 {
		t.Fatalf("a new connection counts %d invitations, %d unread", later.OpenInvitations(), later.UnreadMessages())
	}
}

// -- channels ----------------------------------------------------------------

// channel is a new channel with each client given subscribed to it.
func channel(t *testing.T, demo *Client, subscribers ...*Client) string {
	t.Helper()
	founded, err := demo.CreateChannel("Feed "+t.Name(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range subscribers {
		if _, err := c.Subscribe(founded.ID); err != nil {
			t.Fatal(err)
		}
	}
	return founded.ID
}

func TestAChannelItemIsOpenedAndEveryDeviceAgrees(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	phone := connect(t, server.Base, "alice")
	feed := channel(t, demo, demo, alice, phone)
	if err := demo.Publish(feed, "Deploy", "at four"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the item", func() bool { return len(alice.Log(feed)) == 1 && len(phone.Log(feed)) == 1 })
	if item := alice.Log(feed)[0]; item.Subject == nil || *item.Subject != "Deploy" || item.Body != "at four" {
		t.Fatalf("alice received %+v", item)
	}
	// Opened for its author, and for nobody else yet.
	waitFor(t, "demo's copy", func() bool { return demo.Opened(feed)[1] })
	if alice.Opened(feed)[1] {
		t.Fatal("alice's item arrived opened")
	}

	if err := alice.Open(feed, 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the other device", func() bool { return phone.Opened(feed)[1] })
	// A connection made afterwards learns it from the backfill.
	if later := connect(t, server.Base, "alice"); !later.Opened(feed)[1] {
		t.Fatal("a new connection does not know the item was opened")
	}
}

// Nothing was pushed to a client outside the audience, so subscribing to a
// channel with history has to fetch it.
func TestSubscribingFetchesWhatWasAlreadyPublished(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	feed := channel(t, demo)
	for _, body := range []string{"one", "two"} {
		if err := demo.Publish(feed, "", body); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := alice.Subscribe(feed); err != nil {
		t.Fatal(err)
	}
	if got := bodies(alice.Log(feed)); !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("after subscribing the log is %v", got)
	}
}

func TestAChannelCountsNothingUnread(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	feed := channel(t, demo, alice)
	if err := demo.Publish(feed, "", "news"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the item", func() bool { return len(alice.Log(feed)) == 1 })
	if unread := alice.Unread(feed); unread != 0 {
		t.Fatalf("a channel counts %d unread", unread)
	}
}

func TestAnArchivedPushDropsWhatItNames(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	feed := channel(t, demo, alice)
	for _, body := range []string{"one", "two", "three"} {
		if err := demo.Publish(feed, "", body); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three items", func() bool { return len(alice.Log(feed)) == 3 })
	if err := alice.Open(feed, 1); err != nil {
		t.Fatal(err)
	}

	alice.dispatch(json.RawMessage(`{"type": "archived", "room": "` + feed + `", "through": 2}`))
	if got := bodies(alice.Log(feed)); !slices.Equal(got, []string{"three"}) {
		t.Fatalf("the log is %v", got)
	}
	if opened := alice.Opened(feed); len(opened) != 0 {
		t.Fatalf("opened marks outlived their items: %v", opened)
	}
	alice.mutex.Lock()
	cursor := alice.cursors[feed]
	alice.mutex.Unlock()
	if cursor != 3 {
		t.Fatalf("the cursor moved to %d", cursor)
	}
}
