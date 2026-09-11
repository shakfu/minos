package conformance

// Submissions and moderation: chat-concepts.md section 5. A submission is
// outside its channel's sequence: submitting and rejecting issue no number and
// approval issues the next, so what subscribers read stays contiguous.

import (
	"fmt"
	"testing"
)

const (
	noSubmissions  = "That channel accepts no submissions"
	moderatorsOnly = "Only a moderator may do that"
	decided        = "That submission has been decided"
)

func submissionPush(state string, id any) Pred {
	return func(e Obj) bool {
		submission := obj(e["submission"])
		return e["type"] == "submission" && submission["id"] == id && submission["state"] == state
	}
}

// found is a channel alice moderates, with each socket given subscribed to it.
func found(demo *Socket, subscribers ...*Socket) any {
	channel := demo.Call("channel.create", "title", unique("Curated"), "groups", []string{})
	demo.Call("channel.appoint", "channel", channel["id"], "username", "alice")
	for _, socket := range subscribers {
		socket.Call("subscribe", "channel", channel["id"])
	}
	return channel["id"]
}

// mine is the caller's own open submissions, as sync reports them.
func mine(socket *Socket) map[string]Obj { return byID(socket.Call("sync")["submissions"]) }

func queueOf(socket *Socket, channel any) []any {
	return pluck(socket.Call("channel.queue", "channel", channel)["submissions"], "id")
}

// -- which channels take submissions ------------------------------------------

// A price feed: a producer publishes, an audience reads, nothing to curate.
func TestAChannelWithNoModeratorAcceptsNoSubmissions(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	channel := demo.Call("channel.create", "title", unique("Feed"), "groups", []string{})
	bob.Call("subscribe", "channel", channel["id"])

	same(t, channel["moderators"], []any{})
	same(t, bob.Refuse("channel.submit", "channel", channel["id"], "body", "buy"), noSubmissions)
}

func TestAppointingIsTheAdministratorsAndIsAnnounced(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := demo.Call("channel.create", "title", unique("Curated"), "groups", []string{})
	bob.Call("subscribe", "channel", channel["id"])

	reply := demo.Call("channel.appoint", "channel", channel["id"], "username", "alice")
	same(t, reply["ok"], true)
	same(t, obj(reply["channel"])["moderators"], []string{"alice"})
	// Subscribers are told the channel now takes submissions.
	bob.ExpectPush(func(e Obj) bool {
		pushed := obj(e["room"])
		return e["type"] == "room" && pushed["id"] == channel["id"] &&
			encode(pushed["moderators"]) == `["alice"]`
	})

	refusal := "Only an administrator may do that"
	same(t, alice.Refuse("channel.appoint", "channel", channel["id"], "username", "bob"), refusal)
	same(t, alice.Refuse("channel.dismiss", "channel", channel["id"], "username", "alice"), refusal)
	same(t, demo.Refuse("channel.appoint", "channel", channel["id"], "username", "nobody"),
		"No such user: nobody")
}

func TestARoomHasNoModerators(t *testing.T) {
	demo := connect(t, admin)
	room := demo.Call("open", "invite", []string{}, "title", unique("Room"))
	same(t, room["moderators"], []any{})
	same(t, demo.Refuse("channel.appoint", "channel", room["id"], "username", "alice"),
		fmt.Sprintf("No such room: %v", room["id"]))
}

// Moderators widen the set of producers; they do not replace the admin.
func TestAModeratorPublishesDirectly(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)

	same(t, alice.Call("channel.publish", "channel", channel, "body", "from the desk")["seq"], 1)
	same(t, demo.Call("channel.publish", "channel", channel, "body", "and the admin")["seq"], 2)
	same(t, bob.Refuse("channel.publish", "channel", channel, "body", "me too"),
		"Only an administrator or a moderator may do that")
}

// -- the life of a submission -------------------------------------------------

func TestASubmissionWaitsForAModeratorAndTakesNoSequence(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)

	submission := bob.Call("channel.submit", "channel", channel, "body", "  a tip  ")
	keySet(t, submission, "id", "channel", "author", "body", "at", "state", "comment")
	same(t, []any{submission["channel"], submission["author"]}, []any{channel, "bob"})
	same(t, submission["body"], "a tip")
	same(t, submission["state"], "pending")
	null(t, submission, "comment")

	alice.ExpectPush(submissionPush("pending", submission["id"]))
	same(t, queueOf(alice, channel), []any{submission["id"]})
	same(t, mine(bob)[str(submission["id"])]["state"], "pending")
	// Nothing reached the channel: its sequence has not moved.
	same(t, bob.Call("history", "room", channel, "since", 0)["lastSeq"], 0)
}

func TestApprovalPublishesItAsItsAuthorWroteIt(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "a tip")

	same(t, alice.Call("submission.approve", "submission", submission["id"]), Obj{"ok": true, "seq": 1})

	bob.ExpectPush(submissionPush("approved", submission["id"]))
	messages := list(bob.Call("history", "room", channel, "since", 0)["messages"])
	published := obj(messages[len(messages)-1])
	same(t, []any{published["seq"], published["author"], published["body"], published["kind"]},
		[]any{1, "bob", "a tip", "text"})
	same(t, queueOf(alice, channel), []any{})
	_, open := mine(bob)[str(submission["id"])]
	truth(t, !open, "an approved submission is still open")
}

// What a rejection must not do is cost subscribers a sequence number.
func TestARejectionLeavesNoGap(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	first := bob.Call("channel.submit", "channel", channel, "body", "one")
	second := bob.Call("channel.submit", "channel", channel, "body", "two")

	alice.Call("submission.reject", "submission", first["id"])
	alice.Call("submission.approve", "submission", second["id"])

	messages := bob.Call("history", "room", channel, "since", 0)["messages"]
	same(t, []any{pluck(messages, "seq"), pluck(messages, "body")}, []any{[]int{1}, []string{"two"}})
}

func TestARejectionIsReportedAndKeptUntilAcknowledged(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "nearly")

	same(t, alice.Call("submission.reject", "submission", submission["id"], "comment", " too long "), Obj{"ok": true})

	pushed := obj(bob.ExpectPush(submissionPush("rejected", submission["id"]))["submission"])
	same(t, pushed["comment"], "too long")
	// The text comes back with it, so the author can revise and resubmit.
	same(t, pushed["body"], "nearly")
	same(t, queueOf(alice, channel), []any{})

	same(t, mine(bob)[str(submission["id"])]["state"], "rejected")
	same(t, bob.Call("submission.acknowledge", "submission", submission["id"]), Obj{"ok": true})
	_, open := mine(bob)[str(submission["id"])]
	truth(t, !open, "an acknowledged rejection is still open")
	same(t, bob.Refuse("submission.acknowledge", "submission", submission["id"]),
		fmt.Sprintf("No such submission: %v", submission["id"]))
}

// The reason a rejection is kept rather than deleted when it is made.
func TestAnAuthorWhoWasAwayLearnsTheOutcomeOnReturn(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "while away")
	bob.Close()

	alice.Call("submission.reject", "submission", submission["id"])

	rejection := mine(connect(t, "bob"))[str(submission["id"])]
	same(t, rejection["state"], "rejected")
	null(t, rejection, "comment")
}

// -- who decides ----------------------------------------------------------------

// The moderator set is what makes a channel take submissions, so it is explicit.
func TestOnlyAModeratorDecidesAndAnAdministratorIsNotOne(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	connect(t, "alice")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "mine")

	for _, socket := range []*Socket{demo, bob} {
		same(t, socket.Refuse("channel.queue", "channel", channel), moderatorsOnly)
		same(t, socket.Refuse("submission.approve", "submission", submission["id"]), moderatorsOnly)
		same(t, socket.Refuse("submission.reject", "submission", submission["id"]), moderatorsOnly)
	}
}

func TestADecisionIsFinal(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "once")
	alice.Call("submission.reject", "submission", submission["id"])

	same(t, alice.Refuse("submission.approve", "submission", submission["id"]), decided)
	same(t, alice.Refuse("submission.reject", "submission", submission["id"]), decided)
}

func TestOnlyItsAuthorAcknowledgesAndOnlyOnceDecided(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "waiting")

	same(t, bob.Refuse("submission.acknowledge", "submission", submission["id"]), "That submission is still pending")
	alice.Call("submission.reject", "submission", submission["id"])
	same(t, alice.Refuse("submission.acknowledge", "submission", submission["id"]),
		fmt.Sprintf("No such submission: %v", submission["id"]))
}

func TestDismissingTheLastModeratorRejectsTheQueue(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "orphaned")

	reply := demo.Call("channel.dismiss", "channel", channel, "username", "alice")
	same(t, obj(reply["channel"])["moderators"], []any{})

	pushed := obj(bob.ExpectPush(submissionPush("rejected", submission["id"]))["submission"])
	same(t, pushed["comment"], "That channel no longer accepts submissions")
	same(t, bob.Refuse("channel.submit", "channel", channel, "body", "again"), noSubmissions)
}

func TestDismissingOneOfTwoModeratorsKeepsTheQueue(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	channel := found(demo, bob)
	demo.Call("channel.appoint", "channel", channel, "username", "demo")
	submission := bob.Call("channel.submit", "channel", channel, "body", "still here")

	demo.Call("channel.dismiss", "channel", channel, "username", "alice")
	same(t, queueOf(demo, channel), []any{submission["id"]})
}

func TestSubmittingNeedsTheChannelAndSomethingToSay(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo)
	same(t, bob.Refuse("channel.submit", "channel", channel, "body", "hi"), "Not invited to that room")

	bob.Call("subscribe", "channel", channel)
	same(t, bob.Refuse("channel.submit", "channel", channel, "body", "   "), "Empty message")

	room := bob.Call("open", "invite", []string{}, "title", unique("Room"))
	same(t, bob.Refuse("channel.submit", "channel", room["id"], "body", "hi"),
		fmt.Sprintf("No such room: %v", room["id"]))
	same(t, alice.Refuse("submission.approve", "submission", "absent"), "No such submission: absent")
}
