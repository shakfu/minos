package main

// The broker's arguments, against a server that only counts requests: a
// socket path it cannot use must fail before minosb logs in.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minos/internal/broker"
	"minos/internal/client"
	"minos/internal/testserver"
)

// launch runs minosb against a server that answers every request with 500,
// and reports its exit code, its stderr, and whether the server was reached.
func launch(t *testing.T, socket string) (int, string, bool) {
	t.Helper()
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MINOS_PASSWORD", "p")

	var stderr bytes.Buffer
	code := run([]string{"-server", server.URL, "-user", "u", "-room", "r", "-socket", socket}, &stderr)
	return code, stderr.String(), reached.Load()
}

func TestASocketPathTooLongIsRefusedBeforeTheServer(t *testing.T) {
	code, stderr, reached := launch(t, "/tmp/"+strings.Repeat("x", 200)+"/run.sock")
	if code != 2 || !strings.Contains(stderr, "at most") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if reached {
		t.Fatal("the server was contacted")
	}
}

func TestAPathHoldingAFileIsRefusedBeforeTheServer(t *testing.T) {
	file := filepath.Join(filepath.Dir(testserver.SocketPath(t)), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr, reached := launch(t, file)
	if code != 2 || !strings.Contains(stderr, "not a socket") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if reached {
		t.Fatal("the server was contacted")
	}
}

// The control: a usable path does reach the server, so the refusals above are
// not an artefact of a server minosb never dials.
func TestAUsablePathGoesOnToTheServer(t *testing.T) {
	if _, _, reached := launch(t, "/tmp/minos-unused.sock"); !reached {
		t.Fatal("the server was never contacted")
	}
}

// The run command gets the broker's environment less the credential.
func TestTheRunCommandDoesNotInheritTheCredential(t *testing.T) {
	got := childEnv([]string{
		"PATH=/bin", "MINOS_PASSWORD=secret", "MINOS_USER=worker",
		"MINOS_SERVER=http://x", "MINOS_PASSWORD_HINT=kept",
	})
	want := []string{"PATH=/bin", "MINOS_SERVER=http://x", "MINOS_PASSWORD_HINT=kept"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the run command's environment is %q", got)
	}
}

// An agent that ends its last turn and exits at once still has that turn
// relayed. Wait closes the stdout pipe, so reading must finish before it.
func TestTheLastTurnIsRelayedWhenTheAgentExitsAtOnce(t *testing.T) {
	server := testserver.Start(t, 200)
	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatalf("the developer cannot connect: %v", err)
	}
	t.Cleanup(developer.Stop)
	room, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "bob"}}, "Run", "persisted")
	if err != nil {
		t.Fatalf("cannot open the run's room: %v", err)
	}
	t.Setenv("MINOS_PASSWORD", "bob")

	// One run loses its turn about one time in five, so repeat.
	const runs = 20
	for n := range runs {
		record := fmt.Sprintf(`{"type":"result","result":"final %d"}`, n)
		var stderr bytes.Buffer
		code := run([]string{
			"-server", server.Base, "-user", "bob", "-room", room.ID,
			"-socket", testserver.SocketPath(t), "--", "/bin/echo", record,
		}, &stderr)
		if code != 0 {
			t.Fatalf("run %d exited %d: %s", n, code, stderr.String())
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		relayed := map[string]bool{}
		for _, message := range developer.Log(room.ID) {
			relayed[message.Body] = true
		}
		var missing []string
		for n := range runs {
			if body := fmt.Sprintf("final %d", n); !relayed[body] {
				missing = append(missing, body)
			}
		}
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never relayed: %v", missing)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The dispatcher's control lane: an interrupt with an id reaches the relay,
// and anything else is refused with a reason rather than dropped.
func TestTheControlLaneTakesInterruptsAndRefusesTheRest(t *testing.T) {
	input := strings.Join([]string{
		`{"op":"interrupt","id":"p1"}`,
		``,
		`{"op":"interrupt"}`,
		`{"op":"stop","id":"p2"}`,
		`interrupt`,
	}, "\n")
	var asks []broker.Ask
	var refusals []string
	control(strings.NewReader(input),
		func(ask broker.Ask) { asks = append(asks, ask) },
		func(event map[string]any) { refusals = append(refusals, event["error"].(string)) })

	if len(asks) != 1 || asks[0] != (broker.Ask{By: "dispatcher", ID: "p1"}) {
		t.Fatalf("the relay was asked %v", asks)
	}
	want := []string{"needs an id", `no op "stop"`, "not a JSON object"}
	if len(refusals) != len(want) {
		t.Fatalf("refused %q", refusals)
	}
	for i, fragment := range want {
		if !strings.Contains(refusals[i], fragment) {
			t.Errorf("refusal %d reads %q, want %q", i, refusals[i], fragment)
		}
	}
}

func TestInterruptersAreACommaSeparatedList(t *testing.T) {
	if got := names(" demo, ,alice,"); strings.Join(got, "|") != "demo|alice" {
		t.Fatalf("names answered %q", got)
	}
	if got := names(""); got != nil {
		t.Fatalf("an empty list answered %q", got)
	}
}
