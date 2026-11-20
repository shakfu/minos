package main

// `minos ping` against a real server, and against none.

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"minos/internal/config"
	"minos/internal/testserver"
)

func pingAs(t *testing.T, base, user, password string) (int, string) {
	t.Helper()
	t.Setenv("MINOS_USER", user)
	t.Setenv("MINOS_PASSWORD", password)
	var stdout, stderr bytes.Buffer
	code := ping([]string{"-server", base}, &stdout, &stderr)
	return code, stdout.String()
}

func TestPingTellsAServerFromNoneAndGoodCredentialsFromBad(t *testing.T) {
	base := testserver.Start(t, config.HistoryLimit).Base

	code, out := pingAs(t, base, "worker", "worker")
	var report struct {
		User         string   `json:"user"`
		Capabilities []string `json:"capabilities"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil {
		t.Fatalf("a good ping exited %d with %q", code, out)
	}
	if report.User != "worker" || !slices.Contains(report.Capabilities, "minosb.end") {
		t.Fatalf("the report is %+v", report)
	}

	if code, _ := pingAs(t, base, "worker", "wrong"); code != pingRefused {
		t.Fatalf("a bad password exited %d", code)
	}
	if code, out := pingAs(t, base, "", ""); code != 0 || bytes.Contains([]byte(out), []byte(`"user"`)) {
		t.Fatalf("a ping without credentials exited %d with %q", code, out)
	}
	if code, _ := pingAs(t, "http://127.0.0.1:1", "worker", "worker"); code != pingNoServer {
		t.Fatalf("no server exited %d", code)
	}
}
