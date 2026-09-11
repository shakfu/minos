package timeline

import (
	"errors"
	"path/filepath"
	"testing"
)

// channel is a fresh store holding one channel nobody moderates.
func channel(t *testing.T) (*Timeline, string) {
	t.Helper()
	store, err := open(t, filepath.Join(t.TempDir(), "timeline.db"))
	if err != nil {
		t.Fatalf("cannot open a new database: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	room, err := store.CreateRoom("News", "demo", ChannelKind, Admin, Persisted, "", nil)
	if err != nil {
		t.Fatalf("cannot arrange a channel: %v", err)
	}
	return store, room.ID
}

// moderated is the same channel with alice appointed to it.
func moderated(t *testing.T) (*Timeline, string) {
	t.Helper()
	store, id := channel(t)
	if _, err := store.Appoint(id, "alice"); err != nil {
		t.Fatalf("cannot appoint: %v", err)
	}
	return store, id
}

func submit(t *testing.T, store *Timeline, channelID, author, body string) *Submission {
	t.Helper()
	submission, err := store.Submit(channelID, author, "", body)
	if err != nil || submission == nil {
		t.Fatalf("cannot submit: %v, %v", submission, err)
	}
	return submission
}

func TestAChannelWithNoModeratorStoresNoSubmission(t *testing.T) {
	store, id := channel(t)

	submission, err := store.Submit(id, "bob", "", "hello")
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if submission != nil {
		t.Fatalf("a channel nobody moderates took a submission: %+v", submission)
	}
}

func TestASubmissionTakesNoSequenceUntilApproved(t *testing.T) {
	// The gap rule: a rejected submission must leave nothing for a subscriber to
	// re-request, so neither submitting nor rejecting may issue a number.
	store, id := moderated(t)
	rejected := submit(t, store, id, "bob", "no")
	approved := submit(t, store, id, "bob", "yes")

	if _, err := store.Reject(rejected.ID, nil); err != nil {
		t.Fatalf("cannot reject: %v", err)
	}
	if room, _ := store.Room(id); room.LastSeq != 0 {
		t.Fatalf("submitting and rejecting issued up to seq %d", room.LastSeq)
	}

	message, err := store.Approve(approved.ID)
	if err != nil {
		t.Fatalf("cannot approve: %v", err)
	}
	if message.Seq != 1 || message.Author != "bob" || message.Body != "yes" || message.Kind != Text {
		t.Fatalf("approval published %+v", message)
	}
	if left, _ := store.Submission(approved.ID); left != nil {
		t.Fatal("an approved submission is still stored")
	}
}

func TestADecidedSubmissionCannotBeDecidedAgain(t *testing.T) {
	store, id := moderated(t)
	approved := submit(t, store, id, "bob", "once")

	if _, err := store.Approve(approved.ID); err != nil {
		t.Fatalf("cannot approve: %v", err)
	}
	if _, err := store.Approve(approved.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a second approval gave %v, want ErrNotPending", err)
	}
	if room, _ := store.Room(id); room.LastSeq != 1 {
		t.Fatalf("a second approval issued seq %d", room.LastSeq)
	}

	rejected := submit(t, store, id, "bob", "twice")
	if decided, err := store.Reject(rejected.ID, nil); err != nil || !decided {
		t.Fatalf("cannot reject: %v", err)
	}
	if decided, _ := store.Reject(rejected.ID, nil); decided {
		t.Fatal("a rejected submission was rejected again")
	}
	if _, err := store.Approve(rejected.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approving a rejected submission gave %v", err)
	}
}

func TestARejectionStaysUntilItsAuthorAcknowledgesIt(t *testing.T) {
	store, id := moderated(t)
	submission := submit(t, store, id, "bob", "draft")
	comment := "too long"

	if acknowledged, _ := store.Acknowledge(submission.ID); acknowledged {
		t.Fatal("a pending submission was acknowledged away")
	}
	if _, err := store.Reject(submission.ID, &comment); err != nil {
		t.Fatalf("cannot reject: %v", err)
	}

	mine, err := store.SubmissionsBy("bob")
	if err != nil || len(mine) != 1 || mine[0].State != Rejected ||
		mine[0].Comment == nil || *mine[0].Comment != comment {
		t.Fatalf("the author sees %+v, %v", mine, err)
	}
	if queue, _ := store.Queue(id); len(queue) != 0 {
		t.Fatal("a rejected submission is still in the queue")
	}

	if acknowledged, err := store.Acknowledge(submission.ID); err != nil || !acknowledged {
		t.Fatalf("cannot acknowledge: %v", err)
	}
	if mine, _ := store.SubmissionsBy("bob"); len(mine) != 0 {
		t.Fatalf("an acknowledged rejection is still stored: %+v", mine)
	}
}

func TestRejectingTheWholeQueueLeavesNothingPending(t *testing.T) {
	store, id := moderated(t)
	submit(t, store, id, "bob", "one")
	submit(t, store, id, "carol", "two")
	if _, err := store.Dismiss(id, "alice"); err != nil {
		t.Fatalf("cannot dismiss: %v", err)
	}

	rejected, err := store.RejectAll(id, "gone")
	if err != nil {
		t.Fatalf("cannot reject the queue: %v", err)
	}
	if len(rejected) != 2 || rejected[0].Author != "bob" || rejected[1].Author != "carol" {
		t.Fatalf("rejected %+v", rejected)
	}
	for _, submission := range rejected {
		if submission.State != Rejected || submission.Comment == nil || *submission.Comment != "gone" {
			t.Fatalf("returned %+v, want it marked rejected", submission)
		}
	}
	if queue, _ := store.Queue(id); len(queue) != 0 {
		t.Fatalf("%d submissions still pending", len(queue))
	}
	// And nothing new can arrive: the insert itself checks for a moderator.
	if late, _ := store.Submit(id, "bob", "", "late"); late != nil {
		t.Fatal("a submission landed after the last moderator went")
	}
}
