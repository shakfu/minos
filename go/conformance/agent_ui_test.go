package conformance

// What a room shows of its agents: who wrote each message (section 14), what
// kind of message an agent run posted, what each run is doing (section 15), and
// the decisions a run waits on, told in the room it was raised from (section
// 16). worker is the one agent, as MINOS_AGENTS defaults.

import (
	"strings"
	"testing"
	"time"
)

// runRoom is a room demo raises with worker and alice in it.
func runRoom(t *testing.T, demo *Socket) any {
	t.Helper()
	return demo.Call("open", "invite", []string{"worker", "alice"}, "title", unique("Run"))["id"]
}

func agentPush(room any, state string) Pred {
	return func(e Obj) bool {
		agent := obj(e["agent"])
		return e["type"] == "agent" && agent["room"] == room && agent["state"] == state
	}
}

// -- author kinds --------------------------------------------------------------

func TestEveryMessageSaysWhetherAPersonAnAgentOrTheServerWroteIt(t *testing.T) {
	demo, alice, worker := connect(t, admin), connect(t, "alice"), connect(t, "worker")
	room := runRoom(t, demo)
	alice.Call("send", "room", room, "body", "please fix the build")
	worker.Call("send", "room", room, "body", "on it")
	demo.Call("room.close", "room", room)

	kinds := map[string]string{}
	for _, message := range list(alice.Call("history", "room", room, "since", 0)["messages"]) {
		kinds[str(obj(message)["author"])] = str(obj(message)["authorKind"])
	}
	same(t, kinds, map[string]string{"alice": "user", "worker": "agent", "system": "system"})
}

// -- message kinds -------------------------------------------------------------

func TestAnAgentRunPostsProgressAndHandoffsAndNobodyElseDoes(t *testing.T) {
	demo, alice, worker := connect(t, admin), connect(t, "alice"), connect(t, "worker")
	room := runRoom(t, demo)

	worker.Call("send", "room", room, "body", "Bash go test ./...", "kind", "progress")
	worker.Call("send", "room", room, "body", "stage 2: start from the failing test", "kind", "handoff")
	worker.Call("send", "room", room, "body", "plain", "kind", "text")
	kinds := pluck(alice.Call("history", "room", room, "since", 0)["messages"], "kind")
	same(t, kinds, []string{"progress", "handoff", "text"})

	same(t, alice.Refuse("send", "room", room, "body", "x", "kind", "progress"), "Only an agent run posts progress")
	same(t, alice.Refuse("send", "room", room, "body", "x", "kind", "handoff"), "Only an agent run posts handoff")
	refusal := worker.Refuse("send", "room", room, "body", "x", "kind", "event")
	truth(t, strings.HasPrefix(refusal, "No such message kind: event"), "refused with %q", refusal)
}

// -- agent state ---------------------------------------------------------------

func TestOnlyARunInTheRoomReportsItsState(t *testing.T) {
	demo, alice, worker := connect(t, admin), connect(t, "alice"), connect(t, "worker")
	room := runRoom(t, demo)

	same(t, alice.Refuse("agent.state", "room", room, "state", "idle"), "Only an agent run reports its state")
	same(t, worker.Refuse("agent.state", "room", room, "state", "idle"),
		"Enter the room before reporting a state in it")
	worker.Call("enter", "room", room)
	refusal := worker.Refuse("agent.state", "room", room, "state", "thinking")
	truth(t, strings.HasPrefix(refusal, "No such agent state: thinking"), "refused with %q", refusal)
	same(t, worker.Refuse("agent.state", "room", room, "state", "idle", "detail", strings.Repeat("x", 201)),
		"A detail is at most 200 characters")
}

func TestARunsStateReachesTheRoomAndItsSync(t *testing.T) {
	demo, alice, worker := connect(t, admin), connect(t, "alice"), connect(t, "worker")
	room := runRoom(t, demo)
	worker.Call("enter", "room", room)
	alice.Call("send", "room", room, "body", "first")

	reply := worker.Call("agent.state", "room", room, "state", "working", "turn", 1, "held", 2, "through", 99)
	same(t, reply["ok"], true)
	agent := obj(reply["agent"])
	keySet(t, agent, "room", "username", "state", "turn", "held", "through", "detail", "since", "at")
	same(t, agent["username"], "worker")
	same(t, agent["held"], 2)
	// Nothing past the room's end was handed over.
	same(t, agent["through"], 1)

	pushed := obj(alice.ExpectPush(agentPush(room, "working"))["agent"])
	same(t, pushed["turn"], 1)

	// A report in the same state keeps when the state began.
	time.Sleep(20 * time.Millisecond)
	again := obj(worker.Call("agent.state", "room", room, "state", "working", "turn", 1)["agent"])
	same(t, again["since"], agent["since"])
	truth(t, num(again["at"]) > num(agent["at"]), "at did not move: %v", again["at"])

	agents := list(alice.Call("sync")["agents"])
	truth(t, len(agents) == 1 && obj(agents[0])["room"] == room, "sync carries %v", agents)
}

// A broker that dies reports nothing more. The server says it stopped.
func TestARunThatLeavesItsRoomIsReportedStopped(t *testing.T) {
	server := freshServer(t, nil)
	demo, alice := attach(t, server, "demo"), attach(t, server, "alice")
	room := runRoom(t, demo)
	worker := attach(t, server, "worker")
	worker.Call("enter", "room", room)
	worker.Call("agent.state", "room", room, "state", "working", "turn", 3)
	alice.ExpectPush(agentPush(room, "working"))

	worker.Close()
	stopped := obj(alice.ExpectPush(agentPush(room, "stopped"))["agent"])
	same(t, stopped["detail"], "left the room")
	same(t, stopped["turn"], 3)
}

// -- decisions raised from a room ---------------------------------------------

func TestARequestRaisedFromARoomIsToldThereAndQueuedForItsModerator(t *testing.T) {
	demo, alice, worker := connect(t, admin), connect(t, "alice"), connect(t, "worker")
	room := runRoom(t, demo)
	channel := found(demo, worker)

	submission := worker.Call("channel.submit", "channel", channel, "subject", "Deploy?",
		"body", "to staging", "room", room)
	same(t, submission["room"], room)
	told := alice.ExpectPush(MessageIn(room))
	same(t, told["kind"], "event")
	body := str(told["body"])
	truth(t, strings.HasPrefix(body, "worker asked Curated") && strings.HasSuffix(body, " for a decision: Deploy?"),
		"the room was told %q", body)

	queue := byID(alice.Call("sync")["queue"])
	same(t, queue[str(submission["id"])]["room"], room)

	alice.Call("submission.approve", "submission", submission["id"])
	same(t, alice.ExpectPush(MessageIn(room))["body"], "alice approved worker's request: Deploy?")

	second := worker.Call("channel.submit", "channel", channel, "subject", "Merge?", "body", "x", "room", room)
	alice.ExpectPush(MessageIn(room))
	alice.Call("submission.reject", "submission", second["id"], "comment", "tests fail")
	same(t, alice.ExpectPush(MessageIn(room))["body"], "alice rejected worker's request: Merge? -- tests fail")
}

func TestARequestNamesOnlyARoomItsAuthorIsIn(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	room := runRoom(t, demo)
	channel := found(demo, bob)
	refusal := bob.Refuse("channel.submit", "channel", channel, "body", "x", "room", room)
	truth(t, refusal != "", "bob raised a request from a room he is not in")
	same(t, bob.Call("sync")["submissions"], []any{})
}

func TestAQueueIsTheModeratorsAlone(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")
	channel := found(demo, bob)
	submission := bob.Call("channel.submit", "channel", channel, "body", "a tip")
	alice.ExpectPush(submissionPush("pending", submission["id"]))

	_, queued := byID(alice.Call("sync")["queue"])[str(submission["id"])]
	truth(t, queued, "alice's sync does not queue it")
	_, leaked := byID(bob.Call("sync")["queue"])[str(submission["id"])]
	truth(t, !leaked, "bob's sync queues his own")
}
