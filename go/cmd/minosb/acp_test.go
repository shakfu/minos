package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bytes"
	"fmt"
	"minos/internal/acp/acpfake"
	"minos/internal/client"
	"minos/internal/testserver"
)

// Set, this test binary is the fake ACP agent and runs no tests.
const fakeAgentEnv = "MINOS_ACP_FAKE_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeAgentEnv); dir != "" {
		(&acpfake.Agent{Dir: dir}).Serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// An ACP agent under minosb: the task is the first prompt, a correction the
// second, and /end ends the agent. Each turn is a result event.
func TestAnACPAgentRunsUnderMinosb(t *testing.T) {
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
	t.Setenv(fakeAgentEnv, t.TempDir())
	eventsPath := filepath.Join(t.TempDir(), "events")

	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- run([]string{
			"-server", server.Base, "-user", "bob", "-room", room.ID,
			"-socket", testserver.SocketPath(t), "-events", eventsPath,
			"-agent", "acp", "-task", "begin", "--", os.Args[0],
		}, &lockedBuffer{}, &stderr)
	}()
	waitForBody(t, developer, room.ID, "you said: begin")
	if err := developer.Send(room.ID, "more"); err != nil {
		t.Fatal(err)
	}
	waitForBody(t, developer, room.ID, "you said: demo: more")
	if err := developer.Send(room.ID, "/end"); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("minosb exited %d: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
	var names []string
	for _, event := range eventsIn(t, eventsPath) {
		names = append(names, fmt.Sprint(event["event"]))
	}
	if got := strings.Join(names, " "); got != "ready pushed usage result session turn pushed usage result turn closed exited stopped" {
		t.Fatalf("the events were %s", got)
	}
}

func TestAPermissionPolicyIsDenyOrAllow(t *testing.T) {
	t.Setenv("MINOS_PASSWORD", "p")
	var stderr bytes.Buffer
	code := run([]string{"-server", "http://127.0.0.1:1", "-user", "u", "-room", "r",
		"-socket", "/tmp/minos-unused.sock", "-permission", "ask"}, &lockedBuffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "-permission") {
		t.Fatalf("exited %d: %s", code, stderr.String())
	}
}
