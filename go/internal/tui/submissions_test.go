package tui

// The moderation commands: chat-concepts.md section 5, through the composer.

import (
	"slices"
	"strings"
	"testing"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

func found(t *testing.T, demo *client.Client, subscribers ...*client.Client) string {
	t.Helper()
	channel, err := demo.CreateChannel("Curated", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range subscribers {
		if _, err := c.Subscribe(channel.ID); err != nil {
			t.Fatal(err)
		}
	}
	return channel.ID
}

// moderated is a channel alice moderates, once every subscriber's client knows it.
func moderated(t *testing.T, demo *client.Client, subscribers ...*client.Client) string {
	t.Helper()
	channel := found(t, demo, subscribers...)
	if _, err := demo.Appoint(channel, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, c := range subscribers {
		waitFor(t, "the appointment", func() bool {
			space, ok := c.Space(channel)
			return ok && slices.Equal(space.Moderators, []string{"alice"})
		})
	}
	return channel
}

func three(t *testing.T) (*client.Client, *client.Client, *client.Client) {
	server := testserver.Start(t, config.HistoryLimit)
	return connect(t, server.Base, "demo"), connect(t, server.Base, "alice"), connect(t, server.Base, "bob")
}

func TestAnAppointedModeratorPublishesFromTheComposer(t *testing.T) {
	demo, alice, bob := three(t)
	channel := found(t, demo, alice, bob)

	headless(demo).command("/channel appoint " + channel + " alice")
	waitFor(t, "alice to moderate", func() bool { return alice.Moderates(channel) })

	ui := headless(alice)
	ui.selectSpace(channel)
	if ui.submitting(channel) {
		t.Fatal("a moderator's composer submits")
	}
	ui.compose("from the desk")

	waitFor(t, "the publication", func() bool { return len(bob.Log(channel)) > 0 })
	if last := bob.Log(channel)[len(bob.Log(channel))-1]; last.Author != "alice" {
		t.Fatalf("bob received %+v", last)
	}
}

func TestASubscribersComposerSubmitsAndAModeratorApproves(t *testing.T) {
	demo, alice, bob := three(t)
	channel := moderated(t, demo, alice, bob)

	writer := headless(bob)
	writer.selectSpace(channel)
	if !writer.submitting(channel) {
		t.Fatal("a subscriber's composer publishes")
	}
	writer.compose("a tip")
	writer.mustSay(t, "Submitted")

	moderator := headless(alice)
	moderator.selectSpace(channel)
	moderator.command("/queue")
	listed := moderator.mustSay(t, "a tip")
	moderator.command("/approve " + listed[1:9])

	waitFor(t, "the approval", func() bool { return len(bob.Log(channel)) > 0 })
	if last := bob.Log(channel)[len(bob.Log(channel))-1]; last.Author != "bob" || last.Body != "a tip" {
		t.Fatalf("bob received %+v", last)
	}
	waitFor(t, "bob's submission to close", func() bool { return len(bob.Submissions()) == 0 })
}

func TestARejectionReachesItsAuthorAndAckClosesIt(t *testing.T) {
	demo, alice, bob := three(t)
	channel := moderated(t, demo, alice, bob)
	told := &collector{}
	bob.SetHandlers(func() {}, told.add)

	submission, err := bob.Submit(channel, "nearly")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "alice's queue", func() bool { _, ok := alice.Queued()[submission.ID]; return ok })
	headless(alice).command("/reject " + submission.ID[:8] + " too long")

	waitFor(t, "the rejection", func() bool { return bob.Submissions()[submission.ID].State == "rejected" })
	if comment := bob.Submissions()[submission.ID].Comment; comment == nil || *comment != "too long" {
		t.Fatalf("the comment is %v", comment)
	}
	waitFor(t, "bob to be told", func() bool { return told.has("too long") })

	writer := headless(bob)
	writer.command("/submissions")
	listed := writer.mustSay(t, "nearly")
	if !strings.Contains(listed, "rejected") {
		t.Fatalf("listed %q", listed)
	}
	writer.command("/ack " + submission.ID[:8])
	if _, ok := bob.Submissions()[submission.ID]; ok {
		t.Fatal("the rejection is still held after acknowledging it")
	}
	reply, err := bob.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if reply.Submissions == nil || len(reply.Submissions) != 0 {
		t.Fatalf("sync still reports %v", reply.Submissions)
	}
}

func TestAReturningAuthorFindsTheRejectionWaiting(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice, bob := connect(t, server.Base, "demo"), connect(t, server.Base, "alice"), connect(t, server.Base, "bob")
	channel := moderated(t, demo, alice, bob)
	submission, err := bob.Submit(channel, "while away")
	if err != nil {
		t.Fatal(err)
	}
	bob.Stop()

	if err := alice.Reject(submission.ID, ""); err != nil {
		t.Fatal(err)
	}
	returned := connect(t, server.Base, "bob")
	if state := returned.Submissions()[submission.ID].State; state != "rejected" {
		t.Fatalf("the returning author finds %q", state)
	}
}

func TestDismissingTheLastModeratorIsSaidAndRejectsTheQueue(t *testing.T) {
	demo, alice, bob := three(t)
	channel := moderated(t, demo, alice, bob)
	submission, err := bob.Submit(channel, "orphaned")
	if err != nil {
		t.Fatal(err)
	}

	ui := headless(demo)
	ui.command("/channel dismiss " + channel + " alice")
	ui.mustSay(t, "takes no submissions")
	waitFor(t, "the queue to be rejected", func() bool {
		return bob.Submissions()[submission.ID].State == "rejected"
	})
	waitFor(t, "bob's composer to stop submitting", func() bool { return !headless(bob).submitting(channel) })
}

// No moderators is the core's channel, and the composer treats it so.
func TestAnUnmoderatedChannelPublishesAndIsRefusedAsBefore(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, bob := connect(t, server.Base, "demo"), connect(t, server.Base, "bob")
	channel := found(t, demo, bob)

	ui := headless(bob)
	ui.selectSpace(channel)
	ui.compose("hello")
	ui.mustSay(t, "Only an administrator may do that")
}
