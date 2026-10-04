package broker

// The broker against a real server: a mock of the wire would prove nothing
// about delivery, which is the property the design rests on.
//
// The client's own tests cover a repaired gap and a reported shortfall. What is
// tested here is what the broker does with one: refuse, and keep refusing.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/link"
	"minos/internal/messaging"
	"minos/internal/testserver"
)

// run is one dispatch: a developer, a worker, a room and a decision channel.
type run struct {
	server    *testserver.Server
	developer *client.Client
	worker    *Broker
	room      string
	channel   string
}

func dispatch(t *testing.T, historyLimit int, withChannel bool, interrupters ...string) *run {
	t.Helper()
	server := testserver.Start(t, historyLimit)

	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatalf("the developer cannot connect: %v", err)
	}
	t.Cleanup(developer.Stop)

	room, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "bob"}}, "Run", "persisted")
	if err != nil {
		t.Fatalf("cannot open the run's room: %v", err)
	}

	var channel client.Room
	if withChannel {
		if channel, err = developer.CreateChannel("Decisions", nil, ""); err != nil {
			t.Fatalf("cannot create the decision channel: %v", err)
		}
		if _, err := developer.Appoint(channel.ID, "demo"); err != nil {
			t.Fatalf("cannot appoint the developer: %v", err)
		}
	}

	worker, err := Open(Config{
		Server: server.Base, User: "bob", Password: "bob",
		Room: room.ID, Channel: channel.ID, Interrupters: interrupters,
	})
	if err != nil {
		t.Fatalf("the broker cannot open: %v", err)
	}
	t.Cleanup(worker.Stop)

	return &run{server: server, developer: developer, worker: worker, room: room.ID, channel: channel.ID}
}

// drain waits for the broker to deliver `want` records, and returns them.
func (r *run) drain(t *testing.T, want int) []link.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var records []link.Record
	for time.Now().Before(deadline) {
		reply := r.worker.Handle(link.Request{Op: link.OpMessages})
		records = append(records, reply.Records...)
		if len(records) >= want {
			return records
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited for %d records and saw %d", want, len(records))
	return nil
}

func say(t *testing.T, c *client.Client, room string, bodies ...string) {
	t.Helper()
	for _, body := range bodies {
		if err := c.Send(room, body); err != nil {
			t.Fatalf("cannot send %q: %v", body, err)
		}
	}
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

// -- delivery ----------------------------------------------------------------

func TestTheRoomIsDeliveredAndTheWorkerIsNotToldItsOwnVoice(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	say(t, run.developer, run.room, "start with the tests")

	records := run.drain(t, 1)
	last := records[len(records)-1]
	if last.Author != "demo" || last.Body != "start with the tests" {
		t.Fatalf("delivered %+v", last)
	}

	if reply := run.worker.Handle(link.Request{Op: link.OpSay, Body: "on it"}); reply.Code != link.CodeOK {
		t.Fatalf("say was refused: %s", reply.Error)
	}
	waitFor(t, "the room to carry the reply", func() bool {
		for _, message := range run.developer.Log(run.room) {
			if message.Body == "on it" {
				return true
			}
		}
		return false
	})

	reply := run.worker.Handle(link.Request{Op: link.OpMessages})
	for _, record := range reply.Records {
		if record.Author == "bob" {
			t.Fatalf("the worker was delivered its own message: %+v", record)
		}
	}
}

func TestDeliveryResumesFromTheCursorAndNotFromTheStart(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	say(t, run.developer, run.room, "one", "two")
	run.drain(t, 2)

	say(t, run.developer, run.room, "three")
	records := run.drain(t, 1)
	if len(records) != 1 || records[0].Body != "three" {
		t.Fatalf("a second drain returned %+v", records)
	}

	// An explicit cursor overrides the broker's own, and replays.
	reply := run.worker.Handle(link.Request{Op: link.OpMessages, Since: 1})
	if len(reply.Records) < 2 {
		t.Fatalf("a replay from 1 returned %+v", reply.Records)
	}
}

// A tear is delivery that cannot be repaired: the server's history is capped on
// the tail, so a run far enough behind can never read what it missed. It stops.
func TestATornRunRefusesToAdvanceOverWhatItLost(t *testing.T) {
	run := dispatch(t, 2, false)
	say(t, run.developer, run.room, "one", "two")
	run.drain(t, 2)
	delivered := run.worker.Handle(link.Request{Op: link.OpStatus}).Status.Delivered

	run.worker.tear(run.room, 4)

	reply := run.worker.Handle(link.Request{Op: link.OpMessages})
	if reply.Code != link.CodeGap {
		t.Fatalf("a torn room answered %d: %+v", reply.Code, reply)
	}
	if len(reply.Records) != 1 || reply.Records[0].Kind != link.KindGap || reply.Records[0].Missing != 4 {
		t.Fatalf("the gap reads %+v", reply.Records)
	}

	// The read marker must not move over a gap, and the tear is terminal.
	if reply := run.worker.Handle(link.Request{Op: link.OpProgress, Seq: delivered}); reply.Code != link.CodeRefused {
		t.Fatalf("progress over a tear answered %d", reply.Code)
	}
	if status := run.worker.Handle(link.Request{Op: link.OpStatus}).Status; !status.Torn {
		t.Fatalf("status hides the tear: %+v", status)
	}
	if torn, missing := run.worker.Torn(); !torn || missing != 4 {
		t.Fatalf("Torn reports %v, %d", torn, missing)
	}
}

// A run attached to a room with history is owed what the server retains, on
// request, and nothing on stdin. What the server no longer holds is reported in
// status, not as a tear: the run never had it to lose.
func TestARunAttachedToARoomWithHistoryStartsAtWhatIsRetained(t *testing.T) {
	const retained = 5
	server := testserver.Start(t, retained)
	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(developer.Stop)
	room, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "bob"}}, "Run", "persisted")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		say(t, developer, room.ID, fmt.Sprint("earlier ", i))
	}
	var end int64
	waitFor(t, "the history", func() bool {
		log := developer.Log(room.ID)
		if len(log) > 0 && log[len(log)-1].Body == "earlier 9" {
			end = log[len(log)-1].Seq
			return true
		}
		return false
	})

	worker, err := Open(Config{Server: server.Base, User: "bob", Password: "bob", Room: room.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Stop)

	if pushed, _ := worker.forPush(); len(pushed) != 0 {
		t.Fatalf("history went to stdin: %q", pushed)
	}
	reply := worker.Handle(link.Request{Op: link.OpMessages})
	if reply.Code != link.CodeOK || len(reply.Records) != retained {
		t.Fatalf("messages answered %d with %d records: %+v", reply.Code, len(reply.Records), reply.Records)
	}
	if first := reply.Records[0]; first.Seq != end-retained+1 || first.Body != "earlier 5" {
		t.Fatalf("delivery starts at %+v", first)
	}
	status := worker.Handle(link.Request{Op: link.OpStatus}).Status
	if status.Torn || status.Earlier != end-retained || status.Delivered != end {
		t.Fatalf("status reads %+v; the room ends at %d", status, end)
	}

	say(t, developer, room.ID, "now this")
	waitFor(t, "the new message on stdin", func() bool {
		pushed, _ := worker.forPush()
		return len(pushed) == 1 && pushed[0] == "demo: now this"
	})
}

func TestProgressNeverPassesWhatWasDelivered(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	say(t, run.developer, run.room, "one")
	records := run.drain(t, 1)
	seq := records[0].Seq

	if reply := run.worker.Handle(link.Request{Op: link.OpProgress, Seq: seq + 5}); reply.Code != link.CodeRefused {
		t.Fatalf("progress past delivery answered %d: %+v", reply.Code, reply)
	}
	if reply := run.worker.Handle(link.Request{Op: link.OpProgress, Seq: seq}); reply.Code != link.CodeOK {
		t.Fatalf("progress to %d was refused: %s", seq, reply.Error)
	}
	waitFor(t, "the cursor", func() bool { return run.worker.Chat().ReadCursor(run.room) == seq })
}

// -since re-reads what was delivered; it cannot skip ahead of it. Unchecked,
// it raised the delivered marker past the room's end and progress followed.
func TestSinceCannotSkipPastWhatWasDelivered(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	say(t, run.developer, run.room, "one", "two")
	run.drain(t, 2)
	delivered := run.worker.Handle(link.Request{Op: link.OpStatus}).Status.Delivered

	if reply := run.worker.Handle(link.Request{Op: link.OpMessages, Since: delivered + 1000}); reply.Code != link.CodeRefused {
		t.Fatalf("messages -since past delivery answered %d: %+v", reply.Code, reply)
	}
	if got := run.worker.Handle(link.Request{Op: link.OpStatus}).Status.Delivered; got != delivered {
		t.Fatalf("delivered moved from %d to %d", delivered, got)
	}

	// Re-reading from before the marker still works. Since 0 means "absent".
	reply := run.worker.Handle(link.Request{Op: link.OpMessages, Since: delivered - 1})
	if reply.Code != link.CodeOK || len(reply.Records) != 1 || reply.Records[0].Body != "two" {
		t.Fatalf("a re-read answered %+v", reply)
	}
}

// A restart drops every connection and forgets every session. The broker logs
// in again, and what was said while it was away is delivered, once, with no gap.
func TestARunSurvivesAServerRestartWithoutLosingAMessage(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	say(t, run.developer, run.room, "before")
	run.drain(t, 1)

	run.server.Restart(t)
	// Said inside the broker's reconnect backoff, by a session of its own.
	_, fresh, _, err := client.Connect(run.server.Base, "demo", "demo")
	if err != nil {
		t.Fatalf("cannot connect after the restart: %v", err)
	}
	t.Cleanup(fresh.Stop)
	say(t, fresh, run.room, "while you were away")
	waitFor(t, "the broker to reconnect", run.worker.Chat().Connected)
	say(t, fresh, run.room, "after")

	var got []string
	for _, record := range run.drain(t, 2) {
		got = append(got, record.Body)
	}
	if strings.Join(got, "|") != "while you were away|after" {
		t.Fatalf("delivered %q", got)
	}
	if status := run.worker.Handle(link.Request{Op: link.OpStatus}).Status; status.Torn || !status.Connected {
		t.Fatalf("status after the restart: %+v", status)
	}
}

// -- decisions ---------------------------------------------------------------

func TestASubmissionIsAwaitedUntilItIsApproved(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)

	submitted := run.worker.Handle(link.Request{
		Op: link.OpSubmit, Subject: "install ripgrep", Body: "apt-get install ripgrep",
	})
	if submitted.Code != link.CodeOK || submitted.ID == "" {
		t.Fatalf("submit answered %+v", submitted)
	}

	decided := make(chan link.Reply, 1)
	go func() {
		decided <- run.worker.Handle(link.Request{Op: link.OpAwait, ID: submitted.ID, Timeout: 5})
	}()

	waitFor(t, "the developer to see the submission", func() bool {
		_, queued := run.developer.Queued()[submitted.ID]
		return queued
	})
	if err := run.developer.Approve(submitted.ID); err != nil {
		t.Fatalf("cannot approve: %v", err)
	}

	select {
	case reply := <-decided:
		if reply.State != link.StateApproved {
			t.Fatalf("await answered %+v", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await never returned")
	}
}

func TestARejectionCarriesItsCommentBackToTheWorker(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	submitted := run.worker.Handle(link.Request{Op: link.OpSubmit, Subject: "push", Body: "git push"})

	waitFor(t, "the developer to see the submission", func() bool {
		_, queued := run.developer.Queued()[submitted.ID]
		return queued
	})
	if err := run.developer.Reject(submitted.ID, "nothing is pushed from a run"); err != nil {
		t.Fatalf("cannot reject: %v", err)
	}

	reply := run.worker.Handle(link.Request{Op: link.OpAwait, ID: submitted.ID, Timeout: 5})
	if reply.State != link.StateRejected || reply.Comment != "nothing is pushed from a run" {
		t.Fatalf("await answered %+v", reply)
	}
}

// A wait that runs out is not a decision. The submission is still pending, and
// saying so is what stops a worker acting as though it were refused.
func TestAWaitThatRunsOutIsNotADecision(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	submitted := run.worker.Handle(link.Request{Op: link.OpSubmit, Subject: "wait", Body: "anything"})

	reply := run.worker.Handle(link.Request{Op: link.OpAwait, ID: submitted.ID, Timeout: 0.1})
	if reply.Code != link.CodeOK || reply.State != link.StateTimeout {
		t.Fatalf("a wait that ran out answered %+v", reply)
	}
	// Nor does it leave its waiter behind for a decision that may never come.
	run.worker.mutex.Lock()
	left := len(run.worker.waiting[submitted.ID])
	run.worker.mutex.Unlock()
	if left != 0 {
		t.Fatalf("%d waiter(s) outlived their wait", left)
	}
}

// A timeout too large for a time.Duration is capped. Unchecked, it overflowed
// to a negative duration and the wait ended at once.
func TestAnOversizedTimeoutIsCappedRatherThanEndingAtOnce(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	submitted := run.worker.Handle(link.Request{Op: link.OpSubmit, Subject: "wait", Body: "anything"})

	decided := make(chan link.Reply, 1)
	go func() {
		decided <- run.worker.Handle(link.Request{Op: link.OpAwait, ID: submitted.ID, Timeout: 1e300})
	}()
	select {
	case reply := <-decided:
		t.Fatalf("an oversized wait ended at once: %+v", reply)
	case <-time.After(200 * time.Millisecond):
	}

	waitFor(t, "the developer to see the submission", func() bool {
		_, queued := run.developer.Queued()[submitted.ID]
		return queued
	})
	if err := run.developer.Approve(submitted.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-decided:
		if reply.State != link.StateApproved {
			t.Fatalf("await answered %+v", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await never returned")
	}
}

// A decision pushed before submit's reply returns is kept: the reply's pending
// state must not overwrite it, or await blocks on something already decided.
func TestADecisionPushedBeforeTheSubmitReplyIsKept(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	approved := client.Submission{ID: "s1", Channel: run.channel, Author: "bob", State: link.StateApproved}
	run.worker.decided(approved)
	run.worker.claim(client.Submission{ID: "s1", Channel: run.channel, Author: "bob", State: link.StatePending})

	reply := run.worker.Handle(link.Request{Op: link.OpAwait, ID: "s1", Timeout: 0.1})
	if reply.State != link.StateApproved {
		t.Fatalf("await answered %+v", reply)
	}
}

// Two runs share an account in phase 0, and may share a channel. Neither may
// wait on the other's submission.
func TestARunCannotAwaitASiblingsSubmission(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	sibling, err := Open(Config{
		Server: run.server.Base, User: "bob", Password: "bob",
		Room: run.room, Channel: run.channel,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sibling.Stop)

	theirs := sibling.Handle(link.Request{Op: link.OpSubmit, Subject: "theirs", Body: "x"})
	if theirs.Code != link.CodeOK {
		t.Fatalf("the sibling's submit answered %+v", theirs)
	}
	// The decision is pushed to the author, which is every session of bob's.
	waitFor(t, "the developer to see it", func() bool {
		_, queued := run.developer.Queued()[theirs.ID]
		return queued
	})
	if err := run.developer.Approve(theirs.ID); err != nil {
		t.Fatal(err)
	}
	// Time for the approval push to reach this run's session as well.
	time.Sleep(200 * time.Millisecond)
	if reply := run.worker.Handle(link.Request{Op: link.OpAwait, ID: theirs.ID, Timeout: 0.1}); reply.Code != link.CodeRefused {
		t.Fatalf("a run awaited its sibling's submission: %+v", reply)
	}
}

func TestAWorkerCannotWaitOnSomethingItDidNotSubmit(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	reply := run.worker.Handle(link.Request{Op: link.OpAwait, ID: "deadbeef"})
	if reply.Code != link.CodeRefused {
		t.Fatalf("await on a stranger's submission answered %+v", reply)
	}
}

// Without a decision channel the operation is refused, and the refusal names
// the rule that would permit it.
func TestASubmissionNeedsADecisionChannel(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	reply := run.worker.Handle(link.Request{Op: link.OpSubmit, Subject: "anything", Body: "anything"})
	if reply.Code != link.CodeRefused || reply.Rule == "" {
		t.Fatalf("submit without a channel answered %+v", reply)
	}
}

// -- the six operations ------------------------------------------------------

func TestThereIsNoSeventhOperation(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	for _, op := range []string{"writefile", "settings", "invite", ""} {
		reply := run.worker.Handle(link.Request{Op: op})
		if reply.Code != link.CodeRefused {
			t.Fatalf("%q answered %d: %+v", op, reply.Code, reply)
		}
	}
}

func TestAPayloadTravelsWithTheMessageAndIsCheckedFirst(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)

	reply := run.worker.Handle(link.Request{
		Op: link.OpSay, Body: "requesting a package",
		Payload: json.RawMessage(`{"verb": "install", "package": "ripgrep"}`),
	})
	if reply.Code != link.CodeOK {
		t.Fatalf("say with a payload answered %+v", reply)
	}
	waitFor(t, "the payload in the room", func() bool {
		for _, message := range run.developer.Log(run.room) {
			if strings.Contains(message.Body, payloadFence) && strings.Contains(message.Body, `"ripgrep"`) {
				return true
			}
		}
		return false
	})

	broken := run.worker.Handle(link.Request{
		Op: link.OpSay, Body: "requesting a package", Payload: json.RawMessage(`{verb: install`),
	})
	if broken.Code != link.CodeRefused {
		t.Fatalf("a malformed payload answered %+v", broken)
	}
}

func TestAMessageOverTheServersLimitIsRefusedBeforeItIsSent(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	reply := run.worker.Handle(link.Request{
		Op: link.OpSay, Body: strings.Repeat("x", messaging.BodyLimit+1),
	})
	if reply.Code != link.CodeRefused || !strings.Contains(reply.Error, "work mount") {
		t.Fatalf("an oversized message answered %+v", reply)
	}
}

func TestNothingSaidIsRefusedRatherThanPosted(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	if reply := run.worker.Handle(link.Request{Op: link.OpSay, Body: "  "}); reply.Code != link.CodeRefused {
		t.Fatalf("an empty message answered %+v", reply)
	}
}

func TestStatusReportsTheRunRatherThanTheAccount(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	status := run.worker.Handle(link.Request{Op: link.OpStatus}).Status
	if status == nil || status.Room != run.room || status.Channel != run.channel {
		t.Fatalf("status reads %+v", status)
	}
	if !status.Connected || status.Window != "all" {
		t.Fatalf("status reads %+v", status)
	}
}

// The reader split: one blocked operation must not stop the connection being
// drained, or a worker waiting on a decision stops hearing the developer.
func TestABlockedAwaitDoesNotStopDelivery(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, true)
	submitted := run.worker.Handle(link.Request{Op: link.OpSubmit, Subject: "wait", Body: "anything"})

	blocked := make(chan link.Reply, 1)
	go func() {
		blocked <- run.worker.Handle(link.Request{Op: link.OpAwait, ID: submitted.ID, Timeout: 10})
	}()

	say(t, run.developer, run.room, "stop what you are doing")
	records := run.drain(t, 1)
	if records[len(records)-1].Body != "stop what you are doing" {
		t.Fatalf("delivery during a blocked await returned %+v", records)
	}

	waitFor(t, "the developer to see the submission", func() bool {
		_, queued := run.developer.Queued()[submitted.ID]
		return queued
	})
	if err := run.developer.Approve(submitted.ID); err != nil {
		t.Fatalf("cannot approve: %v", err)
	}
	select {
	case reply := <-blocked:
		if reply.State != link.StateApproved {
			t.Fatalf("the blocked await answered %+v", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked await never returned")
	}
}

// The shim's path, end to end: a request over the run's socket reaches the six
// operations and nothing else.
func TestTheShimReachesTheBrokerOverTheRunsSocket(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	path := testserver.SocketPath(t)
	listener, err := link.Listen(path, 0o600)
	if err != nil {
		t.Fatalf("cannot create the run's socket: %v", err)
	}
	defer listener.Close()
	go link.Serve(listener, run.worker.Handle)

	say(t, run.developer, run.room, "read the tests first")
	waitFor(t, "the message over the socket", func() bool {
		reply, err := link.Do(path, link.Request{Op: link.OpMessages}, 5*time.Second)
		if err != nil {
			t.Fatalf("cannot reach the broker: %v", err)
		}
		for _, record := range reply.Records {
			if record.Body == "read the tests first" {
				return true
			}
		}
		return false
	})

	reply, err := link.Do(path, link.Request{Op: "writefile", Body: "/etc/passwd"}, 5*time.Second)
	if err != nil {
		t.Fatalf("cannot reach the broker: %v", err)
	}
	if reply.Code != link.CodeRefused {
		t.Fatalf("a seventh operation answered %+v", reply)
	}
}

// clientPrincipal is a user, as an invitation names one.
func clientPrincipal(username string) client.Principal {
	return client.Principal{Kind: "user", ID: username}
}
