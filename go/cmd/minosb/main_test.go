package main

// The broker's arguments, against a server that only counts requests: a
// socket path it cannot use must fail before minosb logs in.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	code := run([]string{"-server", server.URL, "-user", "u", "-room", "r", "-socket", socket}, io.Discard, &stderr)
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
		}, io.Discard, &stderr)
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

// -- the agent's stream -------------------------------------------------------

// passRun runs one agent that prints stream under minosb, and returns what
// minosb wrote to stdout and the path of its events file.
func passRun(t *testing.T, stream string, withEvents bool) (stdout, events string, room string, developer *client.Client) {
	t.Helper()
	server := testserver.Start(t, 200)
	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatalf("the developer cannot connect: %v", err)
	}
	t.Cleanup(developer.Stop)
	opened, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "bob"}}, "Run", "persisted")
	if err != nil {
		t.Fatalf("cannot open the run's room: %v", err)
	}
	t.Setenv("MINOS_PASSWORD", "bob")

	dir := t.TempDir()
	agentOutput := filepath.Join(dir, "stream")
	if err := os.WriteFile(agentOutput, []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-server", server.Base, "-user", "bob", "-room", opened.ID, "-socket", testserver.SocketPath(t)}
	events = filepath.Join(dir, "events.jsonl")
	if withEvents {
		args = append(args, "-events", events)
	}
	var out, stderr bytes.Buffer
	if code := run(append(args, "--", "/bin/cat", agentOutput), &out, &stderr); code != 0 {
		t.Fatalf("minosb exited %d: %s", code, stderr.String())
	}
	return out.String(), events, opened.ID, developer
}

// A dispatcher that reads the agent's output sees it unchanged: a line past
// the scanner's first buffer, and a last line with no newline, included.
func TestWithAnEventsFileStdoutIsTheAgentsStreamByteForByte(t *testing.T) {
	stream := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","text":"` + strings.Repeat("a", 70_000) + `"}` + "\n" +
		`{"type":"result","result":"all 41 pass","total_cost_usd":0.12}` + "\n" +
		`trailing words with no newline`
	stdout, events, room, developer := passRun(t, stream, true)

	if stdout != stream {
		t.Fatalf("stdout differs from the agent's stream: %d bytes, want %d", len(stdout), len(stream))
	}
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("an events line is not JSON: %q", line)
		}
		names = append(names, event["event"].(string))
	}
	if names[0] != "ready" || names[len(names)-1] != "stopped" || !slices.Contains(names, "exited") {
		t.Fatalf("the events file holds %v", names)
	}

	// The relay still reads the stream it passes on.
	waitForBody(t, developer, room, "all 41 pass")
}

// Without -events the contract is unchanged: stdout holds control events and
// nothing of the agent's.
func TestWithoutAnEventsFileStdoutHoldsOnlyControlEvents(t *testing.T) {
	stream := `{"type":"result","result":"done"}` + "\n"
	stdout, events, _, _ := passRun(t, stream, false)

	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil || event["event"] == nil {
			t.Fatalf("stdout holds a line that is not a control event: %q", line)
		}
	}
	if _, err := os.Stat(events); !os.IsNotExist(err) {
		t.Fatalf("an events file was written: %v", err)
	}
}

func TestAnEventsFileItCannotOpenIsRefusedBeforeTheServer(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MINOS_PASSWORD", "p")

	var stderr bytes.Buffer
	code := run([]string{"-server", server.URL, "-user", "u", "-room", "r",
		"-socket", "/tmp/minos-unused.sock", "-events", "/nonexistent/dir/events"}, io.Discard, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "-events") || reached.Load() {
		t.Fatalf("exit %d, stderr %q, reached %v", code, stderr.String(), reached.Load())
	}
}

func waitForBody(t *testing.T, c *client.Client, room, body string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range c.Log(room) {
			if message.Body == body {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the room never heard %q", body)
}

// The relay may report a turn while the main loop exits. Nothing follows
// `stopped`, which is where a dispatcher stops reading.
func TestNothingIsReportedAfterStopped(t *testing.T) {
	var out bytes.Buffer
	report := reporter(&out)
	report(map[string]any{"event": "exited", "code": 0})
	report(map[string]any{"event": "stopped", "reason": "exited"})
	report(map[string]any{"event": "turn", "delivering": 0})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"stopped"`) {
		t.Fatalf("reported %q", lines)
	}
}

// -- the task -----------------------------------------------------------------

// The task is the agent's first stdin frame, framed as any push is, so a run
// command needs no wrapper to deliver it.
func TestTheTaskIsTheFirstFrameOnTheAgentsStdin(t *testing.T) {
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

	first := filepath.Join(t.TempDir(), "first")
	agent := `read -r line; printf '%s\n' "$line" > "$1"; echo '{"type":"result","result":"got it"}'`
	task := "Fix \"the parser\".\nThen run make test."
	var stderr bytes.Buffer
	code := run([]string{
		"-server", server.Base, "-user", "bob", "-room", room.ID,
		"-socket", testserver.SocketPath(t), "-task", task,
		"--", "/bin/sh", "-c", agent, "agent", first,
	}, io.Discard, &stderr)
	if code != 0 {
		t.Fatalf("minosb exited %d: %s", code, stderr.String())
	}

	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("the agent read nothing: %v", err)
	}
	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("the first frame is not JSON: %q", raw)
	}
	if frame.Type != "user" || frame.Message.Role != "user" ||
		len(frame.Message.Content) != 1 || frame.Message.Content[0].Text != task {
		t.Fatalf("the first frame is %s", raw)
	}
	waitForBody(t, developer, room.ID, "got it")
}

func TestATaskWithNoRunCommandIsRefusedBeforeTheServer(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MINOS_PASSWORD", "p")

	var stderr bytes.Buffer
	code := run([]string{"-server", server.URL, "-user", "u", "-room", "r",
		"-socket", "/tmp/minos-unused.sock", "-task", "do it"}, io.Discard, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "-task needs a run command") || reached.Load() {
		t.Fatalf("exit %d, stderr %q, reached %v", code, stderr.String(), reached.Load())
	}
}

// -- the linger ---------------------------------------------------------------

// An agent that reads its stdin to the end, as claude does with
// --input-format stream-json, ends the run once -linger closes it.
func TestALingerEndsARunWhoseAgentWaitsForInput(t *testing.T) {
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

	agent := `while read -r line; do echo '{"type":"result","result":"done"}'; done; exit 3`
	var out, stderr bytes.Buffer
	code := run([]string{
		"-server", server.Base, "-user", "bob", "-room", room.ID,
		"-socket", testserver.SocketPath(t), "-task", "do it", "-linger", "100ms",
		"--", "/bin/sh", "-c", agent,
	}, &out, &stderr)
	if code != 3 {
		t.Fatalf("minosb exited %d, not the agent's 3: %s", code, stderr.String())
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil {
			names = append(names, fmt.Sprint(event["event"]))
		}
	}
	closed, exited := slices.Index(names, "closed"), slices.Index(names, "exited")
	if closed < 0 || exited < closed {
		t.Fatalf("the events were %v", names)
	}
}

func TestALingerIsRefusedWithoutARunCommandOrBelowZero(t *testing.T) {
	t.Setenv("MINOS_PASSWORD", "p")
	for _, args := range [][]string{{"-linger", "1s"}, {"-linger", "-1s", "--", "true"}} {
		var stderr bytes.Buffer
		base := []string{"-server", "http://127.0.0.1:1", "-user", "u", "-room", "r", "-socket", "/tmp/minos-unused.sock"}
		code := run(append(base, args...), io.Discard, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "-linger") {
			t.Fatalf("%v: exited %d: %s", args, code, stderr.String())
		}
	}
}
