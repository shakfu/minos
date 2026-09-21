package main

// The broker's arguments, against a server that only counts requests: a
// socket path it cannot use must fail before minosb logs in.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
