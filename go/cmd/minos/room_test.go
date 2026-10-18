package main

// `minos room ensure` against a real server.

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

// ensureAs runs the command as user, whose password is their name.
func ensureAs(t *testing.T, base, user string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("MINOS_USER", user)
	t.Setenv("MINOS_PASSWORD", user)
	var stdout, stderr bytes.Buffer
	code := room(append([]string{"ensure", "-server", base}, args...), &stdout, &stderr)
	return code, strings.TrimSpace(stdout.String()), stderr.String()
}

// observe connects as demo and answers the room's current state.
func observe(t *testing.T, base, id string) client.Room {
	t.Helper()
	_, demo, _, err := client.Connect(base, "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Stop()
	found, ok := demo.Rooms()[id]
	if !ok {
		t.Fatalf("demo cannot see room %s", id)
	}
	return found
}

func TestEnsureFoundsAFiledTaskRoomOnceAndPrintsItsID(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)

	code, first, stderr := ensureAs(t, server.Base, "demo", "-project", "pma", "-task", "23", "-invite", "worker")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	code, second, stderr := ensureAs(t, server.Base, "demo", "-project", "pma", "-task", "23", "-invite", "worker")
	if code != 0 || second != first {
		t.Fatalf("the second ensure answered %q (exit %d, %s), the first %q", second, code, stderr, first)
	}

	found := observe(t, server.Base, first)
	if found.Title != "task/23" || found.Scope != "task" || found.Task != "23" || found.Project == "" {
		t.Fatalf("the room is %+v", found)
	}
	if !granted(found, client.Principal{Kind: "user", ID: "worker"}) {
		t.Fatalf("worker is not invited: %v", found.Grants)
	}
}

// A found room gains the invites it lacks, and a closed one is reopened for
// the rework.
func TestEnsureInvitesWhoIsMissingAndReopensAClosedRoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	code, id, stderr := ensureAs(t, server.Base, "demo", "-project", "pma", "-task", "7")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	_, demo, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.SetState(id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := demo.CreateGroup("reviewers", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	demo.Stop()

	code, again, stderr := ensureAs(t, server.Base, "demo", "-project", "pma", "-task", "7",
		"-invite", "worker", "-invite", "@reviewers", "-tag", "rust")
	if code != 0 || again != id {
		t.Fatalf("exit %d, id %q against %q: %s", code, again, id, stderr)
	}
	found := observe(t, server.Base, id)
	if !found.Open() {
		t.Fatal("the room is still closed")
	}
	if !granted(found, client.Principal{Kind: "user", ID: "worker"}) || len(found.Grants) != 3 {
		t.Fatalf("the grants are %v", found.Grants)
	}
}

func TestEnsureRefusesANonAdministrator(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	code, out, stderr := ensureAs(t, server.Base, "bob", "-project", "pma", "-task", "1")
	if code != 2 || out != "" || !strings.Contains(stderr, "administrator") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}

func TestEnsureNeedsAProjectAndATask(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	if code, _, stderr := ensureAs(t, server.Base, "demo", "-project", "pma"); code != 2 || !strings.Contains(stderr, "-task") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// Two dispatches of one task race to found its room and end in the same one.
func TestConcurrentEnsuresAgreeOnOneRoom(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	t.Setenv("MINOS_USER", "demo")
	t.Setenv("MINOS_PASSWORD", "demo")

	const racers = 4
	ids := make([]string, racers)
	var wait sync.WaitGroup
	for i := range racers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var stdout, stderr bytes.Buffer
			if code := room([]string{"ensure", "-server", server.Base, "-project", "pma", "-task", "9"}, &stdout, &stderr); code != 0 {
				t.Errorf("racer %d: exit %d: %s", i, code, stderr.String())
			}
			ids[i] = strings.TrimSpace(stdout.String())
		}()
	}
	wait.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatalf("the racers answered %q", ids)
		}
	}
}
