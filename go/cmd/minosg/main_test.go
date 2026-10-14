package main

// The gateway's arguments: a listener another host could reach, or a missing
// token, must fail before minosg logs in.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func launch(t *testing.T, token string, args ...string) (int, string, bool) {
	t.Helper()
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("MINOS_PASSWORD", "p")
	t.Setenv("MINOS_A2A_TOKEN", token)

	var stderr bytes.Buffer
	base := []string{"-server", server.URL, "-user", "u", "-room", "r", "-worker", "w"}
	code := run(append(base, args...), &stderr)
	return code, stderr.String(), reached.Load()
}

func TestARoutableListenerIsRefusedBeforeTheServer(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8100", ":8100", "192.168.1.2:8100", "example.com:8100"} {
		code, stderr, reached := launch(t, "t", "-listen", address)
		if code != 2 || !strings.Contains(stderr, "loopback") {
			t.Fatalf("%s: exit %d, stderr %q", address, code, stderr)
		}
		if reached {
			t.Fatalf("%s: the server was contacted", address)
		}
	}
}

func TestATokenIsRequired(t *testing.T) {
	code, stderr, reached := launch(t, "")
	if code != 2 || !strings.Contains(stderr, "MINOS_A2A_TOKEN") || reached {
		t.Fatalf("exit %d, stderr %q, reached %v", code, stderr, reached)
	}
}

func TestLoopbackAddressesAreAccepted(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0", "localhost:0"} {
		if err := loopback(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
}
