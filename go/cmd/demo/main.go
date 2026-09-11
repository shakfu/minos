// Command demo is a narrated run of the channel audience rule against the
// compiled server: its own database, three real websockets, and what each
// client sees at every step. Run it with `make demo`.
//
// The launch and the socket come from minos/conformance, so the demo speaks
// what the contract currently says rather than a copy that could drift.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"minos/conformance"
)

const system = "system"

var passwords = map[string]string{"demo": "demo", "alice": "alice", "bob": "bob"}

// Long enough for a push to have been written and read back.
const settle = 500 * time.Millisecond

// failure panics, so the deferred Stop still runs before the process exits.
type failure struct{}

func (failure) Helper() {}

func (failure) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

type obj = conformance.Obj

// command is MINOS_CONFORMANCE_CMD, or the minosd `make demo` has just built.
func command() []string {
	if argv := conformance.Command(); argv != nil {
		return argv
	}
	_, source, _, _ := runtime.Caller(0)
	return []string{filepath.Join(filepath.Dir(source), "..", "..", "minosd")}
}

func say(line string) { fmt.Println(line) }

func step(number int, title string) {
	say("")
	line := fmt.Sprintf("-- %d. %s ", number, title)
	say(line + strings.Repeat("-", max(0, 72-len(line))))
}

// repr renders a JSON value the way the narration quotes it.
func repr(value any) string {
	switch value := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + value + "'"
	case []any:
		items := make([]string, len(value))
		for i, item := range value {
			items[i] = repr(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		return fmt.Sprint(value)
	}
}

func short(value any) string {
	text := fmt.Sprint(value)
	return text[:min(8, len(text))]
}

// attach is one logged-in client: the HTTP session and the socket over it.
func attach(server *conformance.Server, username string) (*conformance.Http, *conformance.Socket) {
	h := conformance.NewHttp(failure{}, server.Base)
	h.Login(username, passwords[username])
	socket := conformance.Dial(failure{}, server.Base, h.CookieHeader())
	socket.Handshake()
	socket.Call("sync")
	return h, socket
}

func channelSeenBy(socket *conformance.Socket) obj {
	channels, _ := socket.Call("sync")["channels"].([]any)
	for _, channel := range channels {
		if channel := channel.(obj); channel["id"] == system {
			return channel
		}
	}
	return nil
}

func report(name string, socket *conformance.Socket) {
	if channel := channelSeenBy(socket); channel == nil {
		say(fmt.Sprintf("   %-6s does not see the channel at all", name))
	} else {
		say(fmt.Sprintf("   %-6s sees it, audience %s", name, repr(channel["audience"])))
	}
}

func latestIn(socket *conformance.Socket, room any) any {
	messages, _ := socket.Call("history", "room", room, "since", 0)["messages"].([]any)
	if len(messages) == 0 {
		return nil
	}
	return messages[len(messages)-1].(obj)["body"]
}

func latest(socket *conformance.Socket) any { return latestIn(socket, system) }

func run(state string) {
	server, err := conformance.Launch(state, command(), nil)
	if err != nil {
		panic(err)
	}
	defer server.Stop()
	say(fmt.Sprintf("go/minosd on %s, its own database under %s", server.Base, state))

	demoHttp, demo := attach(server, "demo")
	_, alice := attach(server, "alice")
	_, bob := attach(server, "bob")

	step(1, "every account is subscribed to `system` at start-up")
	report("demo", demo)
	report("alice", alice)
	report("bob", bob)

	step(2, "the admin makes a group and restricts the channel to it")
	group := demo.Call("group.create", "name", "Ops", "members", []string{"alice"})
	say(fmt.Sprintf("   /group new Ops alice        ->  @Ops %s %s", short(group["id"]), repr(group["members"])))
	channel := demo.Call("channel.admit", "channel", system, "group", group["id"])["channel"].(obj)
	say("   /channel admit system Ops")
	say(fmt.Sprintf("   restrictedTo %s   audience %s",
		short(channel["restrictedTo"].([]any)[0]), repr(channel["audience"])))

	step(3, "the people it excludes are told once, and refused after")
	bob.ExpectPush(func(e obj) bool { return e["type"] == "roomGone" && e["room"] == system })
	say("   bob's client received: roomGone system")
	report("alice", alice)
	report("bob", bob)
	say("   bob subscribing again  ->  " + repr(bob.Refuse("subscribe", "channel", system)))
	say("   bob reading history    ->  " + repr(bob.Refuse("history", "room", system, "since", 0)))

	step(4, "delivery follows the rule, not the subscription")
	demoHttp.Upload("home:/report.txt", []byte("x"))
	say("   demo writes home:/report.txt, which the server announces on `system`")
	time.Sleep(settle)
	say("   alice  reads " + repr(latest(alice)))
	say("   bob    is refused the history he could read ten seconds ago")

	step(5, "re-admission restores it, and bob does nothing")
	demo.Call("group.assign", "group", group["id"], "username", "bob")
	say("   /group add Ops bob")
	report("bob", bob)
	say("   bob    reads " + repr(latest(bob)))
	say("   his subscription was never deleted, so there was nothing to redo")

	step(6, "a channel can be founded restricted, and written to")
	notices := demo.Call("channel.create", "title", "Ops notices", "groups", []any{group["id"]})
	say(fmt.Sprintf("   /channel new 'Ops notices' @Ops   ->  %s", short(notices["id"])))
	say("   announced on `system`, since nobody is subscribed to it yet")
	alice.Call("subscribe", "channel", notices["id"])
	demo.Call("channel.publish", "channel", notices["id"], "body", "deploy at four")
	time.Sleep(settle)
	say("   alice  subscribes and reads " + repr(latestIn(alice, notices["id"])))
	// demo founded it, publishes to it, and is in no group it admits.
	say("   demo   subscribing        ->  " + repr(demo.Refuse("subscribe", "channel", notices["id"])))
	say("   founding a channel and being in its audience are different things")

	step(7, "the rule can be withdrawn: no groups is open, not closed")
	channel = demo.Call("channel.revoke", "channel", system, "group", group["id"])["channel"].(obj)
	say("   /channel revoke system Ops")
	audience := []any{}
	for _, name := range channel["audience"].([]any) {
		audience = append(audience, name)
	}
	slices.SortFunc(audience, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	say(fmt.Sprintf("   restrictedTo %s   audience %s", repr(channel["restrictedTo"]), repr(audience)))
	report("bob", bob)
}

func main() {
	state, err := os.MkdirTemp("", "minos-demo-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(state)
	run(state)
}
