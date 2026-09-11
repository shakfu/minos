package conformance

// The socket transport: the upgrade, the envelope, and what is refused. A bad
// frame costs the sender a frame rather than a connection.

import (
	"testing"
	"time"
)

// Policy violation: the upgrade completes and the server then closes.
const closePolicyViolation = 1008

// Long enough to prove a frame did not arrive; every local send is answered in
// milliseconds when it is answered at all.
const silence = time.Second

func TestAnAnonymousUpgradeIsClosed(t *testing.T) {
	socket := dial(t, shared.Base, "")
	select {
	case <-socket.Closed:
	case <-time.After(5 * time.Second):
		t.Fatal("an anonymous socket was left open")
	}
	same(t, []any{int(socket.CloseCode), socket.CloseReason}, []any{closePolicyViolation, "Not authenticated"})
}

func TestTheHandshakeIsTheFirstFrame(t *testing.T) {
	h := session(t, "demo")
	socket := dial(t, shared.Base, h.CookieHeader())
	frame := socket.Handshake()
	same(t, frame["name"], "osjs/core:connected")
	maxAge := num(obj(obj(list(frame["params"])[0])["cookie"])["maxAge"])
	truth(t, maxAge > 0, "maxAge is %v", maxAge)
}

// Several requests in flight are told apart by nothing else.
func TestAReplyQuotesThePidItWasSentWith(t *testing.T) {
	demo := connect(t, admin)
	first := demo.SendOp("sync")
	second := demo.SendOp("sync")
	truth(t, demo.AwaitReply(second) != nil, "no reply to the second")
	truth(t, demo.AwaitReply(first) != nil, "no reply to the first")
}

func TestAPushCarriesANullPidAndTheApplicationName(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	room := demo.Call("create", "title", unique("Push"), "invite", []string{"alice"})
	alice.Drain()
	demo.Call("send", "room", room["id"], "body", "hello")

	// The reader sorts by pid, so anything reaching Pushes had a null one.
	same(t, alice.ExpectPush(PushOf("message"))["body"], "hello")
}

func TestAMalformedFrameIsDroppedWithoutClosingTheSocket(t *testing.T) {
	frames := []string{
		"not json at all",
		`{"params": []}`,
		`{"name": 7, "params": []}`,
		`{"name": "` + ApplicationMessage + `", "params": "not a list"}`,
		`{"name": "` + ApplicationMessage + `", "params": []}`,
		`{"name": "` + ApplicationMessage + `", "params": ["not an object"]}`,
		`[1, 2, 3]`,
	}
	for _, frame := range frames {
		t.Run(frame, func(t *testing.T) {
			demo := connect(t, admin)
			demo.SendFrame(frame)
			same(t, demo.Call("sync")["me"], "demo")
		})
	}
}

// A page must not fabricate an event the server trusts. Refused as forged or
// dropped as unhandled look the same from here; a handler for it may not exist.
func TestAnInternalMessageNameDoesNothing(t *testing.T) {
	demo := connect(t, admin)
	demo.SendFrame(Obj{"name": "osjs/core:logged-in", "params": []any{Obj{"username": "root"}}})
	time.Sleep(silence)
	truth(t, demo.Control.Empty(), "a forged name was answered")
	same(t, demo.Call("sync")["me"], "demo")
}

func TestAnUnhandledNameIsDropped(t *testing.T) {
	demo := connect(t, admin)
	demo.SendFrame(Obj{"name": "something/else", "params": []any{}})
	same(t, demo.Call("sync")["me"], "demo")
}

func TestAnApplicationWithNoHandlerAnswersNothing(t *testing.T) {
	demo := connect(t, admin)
	demo.SendFrame(Obj{
		"name":   ApplicationMessage,
		"params": []any{Obj{"pid": 4242, "name": "NoSuchApp", "args": []any{Obj{}}}},
	})
	time.Sleep(silence)
	truth(t, demo.Pushes.Empty(), "an unknown application was answered")
	same(t, demo.Call("sync")["me"], "demo")
}

// One bad field must not cost a client its connection.
func TestAHandlerThatFailsAnswersAnErrorRatherThanClosing(t *testing.T) {
	demo := connect(t, admin)
	same(t, demo.Refuse("open", "invite", 7), "Request failed")
	same(t, demo.Call("sync")["me"], "demo")
}

func TestTheKeepaliveArrivesAfterASilence(t *testing.T) {
	server := freshServer(t, map[string]string{"MINOS_WS_PING": "1"})
	h := login(t, server, "demo")

	socket := dial(t, server.Base, h.CookieHeader())
	socket.Handshake()
	frame := socket.ExpectControl(func(f Obj) bool { return f["name"] == "osjs/core:ping" }, 5*time.Second)
	same(t, frame["params"], []any{})
}
