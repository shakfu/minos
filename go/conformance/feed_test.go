package conformance

// The channel feed: docs/wire-contract.md section 10. A channel message has a
// subject and a body, and each subscriber opens items one at a time.

import (
	"fmt"
	"strings"
	"testing"
)

const tooLong = "A subject is at most 200 characters"

// feed is a new channel with each socket given subscribed to it.
func feed(demo *Socket, subscribers ...*Socket) any {
	channel := demo.Call("channel.create", "title", unique("Feed"), "groups", []string{})
	for _, socket := range subscribers {
		socket.Call("subscribe", "channel", channel["id"])
	}
	return channel["id"]
}

// newest is the last live message in a space, as its history reports it.
func newest(t *testing.T, socket *Socket, room any) Obj {
	t.Helper()
	messages := list(socket.Call("history", "room", room, "since", 0)["messages"])
	truth(t, len(messages) > 0, "%v has no messages", room)
	return obj(messages[len(messages)-1])
}

func opened(socket *Socket, channel any) any {
	return socket.Call("history", "room", channel, "since", 0)["opened"]
}

func messageSeq(room any, seq int) Pred {
	return func(e Obj) bool {
		return e["type"] == "message" && e["room"] == room && encode(e["seq"]) == fmt.Sprint(seq)
	}
}

// -- subjects -----------------------------------------------------------------

func TestAChannelMessageTakesItsSubjectFromTheFirstLine(t *testing.T) {
	demo := connect(t, admin)
	channel := feed(demo, demo)
	demo.Call("channel.publish", "channel", channel, "body", "\n  Deploy at four\nDetails follow")

	message := newest(t, demo, channel)
	same(t, message["subject"], "Deploy at four")
	truth(t, strings.Contains(str(message["body"]), "Details follow"), "the body lost its rest: %v", message["body"])
}

func TestAGivenSubjectIsKeptTrimmed(t *testing.T) {
	demo := connect(t, admin)
	channel := feed(demo, demo)
	demo.Call("channel.publish", "channel", channel, "subject", "  Outage  ", "body", "The mail server is down.")

	message := newest(t, demo, channel)
	same(t, []any{message["subject"], message["body"]}, []any{"Outage", "The mail server is down."})
}

func TestASubjectIsAtMost200Characters(t *testing.T) {
	demo := connect(t, admin)
	channel := feed(demo, demo)
	long := strings.Repeat("x", 250)

	// A derived subject is cut; a given one is refused rather than cut.
	demo.Call("channel.publish", "channel", channel, "body", long+"\nrest")
	same(t, newest(t, demo, channel)["subject"], long[:200])
	same(t, demo.Refuse("channel.publish", "channel", channel, "subject", long[:201], "body", "b"), tooLong)
	demo.Call("channel.publish", "channel", channel, "subject", long[:200], "body", "b")
}

// A headline alone is a message; nothing at all is not.
func TestAHeadlineNeedsNoBody(t *testing.T) {
	demo := connect(t, admin)
	channel := feed(demo, demo)
	demo.Call("channel.publish", "channel", channel, "subject", "Headline", "body", "")

	message := newest(t, demo, channel)
	same(t, []any{message["subject"], message["body"]}, []any{"Headline", ""})
	same(t, demo.Refuse("channel.publish", "channel", channel, "subject", " ", "body", " "), "Empty message")
}

func TestARoomMessageHasNoSubject(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Room"))
	alice.Call("send", "room", room["id"], "body", "Hello\nthere")
	null(t, newest(t, alice, room["id"]), "subject")
}

func TestASubmissionCarriesItsSubjectIntoTheChannel(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)

	submission := bob.Call("channel.submit", "channel", channel, "subject", "A tip", "body", "details")
	same(t, submission["subject"], "A tip")
	derived := bob.Call("channel.submit", "channel", channel, "body", "First line\nsecond")
	same(t, derived["subject"], "First line")

	alice.Call("submission.approve", "submission", submission["id"])
	same(t, newest(t, bob, channel)["subject"], "A tip")
	// Its author wrote it, so it was never pending for them.
	same(t, opened(bob, channel), []int{1})
}

func TestASystemEventTakesItsLineAsItsSubject(t *testing.T) {
	alice := connect(t, "alice")
	name := unique("subject")
	line := "alice wrote home:/" + name + ".txt"
	session(t, "alice").Upload("home:/"+name+".txt", []byte("x"))

	event := alice.ExpectPush(func(e Obj) bool { return e["type"] == "message" && e["body"] == line })
	same(t, []any{event["room"], event["subject"]}, []any{systemChannel, line})
}

// -- opening ------------------------------------------------------------------

func TestOpeningIsPerSubscriberAndIdempotent(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := feed(demo, alice, bob)
	demo.Call("channel.publish", "channel", channel, "body", "one")
	demo.Call("channel.publish", "channel", channel, "body", "two")

	for range 2 {
		same(t, alice.Call("channel.open", "channel", channel, "seq", 2),
			Obj{"ok": true, "channel": channel, "seq": 2})
	}
	same(t, opened(alice, channel), []int{2})
	same(t, opened(bob, channel), []any{})
}

func TestAPublishersOwnMessageIsOpenedForThem(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	channel := feed(demo, demo, bob)
	demo.Call("channel.publish", "channel", channel, "body", "mine")

	same(t, opened(demo, channel), []int{1})
	same(t, opened(bob, channel), []any{})
}

// Opening is the same fact from every device, and nobody else's business.
func TestOpeningReachesTheCallersOtherConnectionsOnly(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	other := connect(t, "alice")
	channel := feed(demo, alice, bob)
	demo.Call("channel.publish", "channel", channel, "body", "one")
	bob.ExpectPush(messageSeq(channel, 1))

	alice.Call("channel.open", "channel", channel, "seq", 1)
	same(t, other.ExpectPush(PushOf("opened")), Obj{"type": "opened", "channel": channel, "seq": 1})

	demo.Call("channel.publish", "channel", channel, "body", "two")
	_, before := bob.CollectPush(messageSeq(channel, 2))
	for _, event := range before {
		truth(t, obj(event)["type"] != "opened", "bob was told of alice's opening: %s", encode(event))
	}
}

func TestOpeningIsRefusedWhereThereIsNothingToOpen(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := feed(demo, alice)
	demo.Call("channel.publish", "channel", channel, "body", "one")

	same(t, alice.Refuse("channel.open", "channel", channel, "seq", 2), "No such message: 2")
	same(t, alice.Refuse("channel.open", "channel", systemChannel, "seq", 1), "Nothing in system is opened")
	same(t, bob.Refuse("channel.open", "channel", channel, "seq", 1),
		bob.Refuse("history", "room", channel, "since", 0))
	room := alice.Call("open", "invite", []string{}, "title", unique("Room"))
	same(t, alice.Refuse("channel.open", "channel", room["id"], "seq", 1),
		fmt.Sprintf("No such room: %v", room["id"]))
}

// A single high-water mark cannot record items opened in any order.
func TestReadIsRefusedOnAChannel(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	channel := feed(demo, alice)
	demo.Call("channel.publish", "channel", channel, "body", "one")

	same(t, alice.Refuse("read", "room", channel, "seq", 1), "A channel's items are opened, not read")
	_, listed := obj(alice.Call("sync")["read"])[str(channel)]
	truth(t, !listed, "sync reports a read cursor for a channel")
}
