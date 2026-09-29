// Package testserver runs a whole minos server in process, for tests that need a
// real HTTP server and a real websocket rather than a mock of either.
package testserver

import (
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minos/internal/chat"
	"minos/internal/config"
	"minos/internal/httpapi"
	"minos/internal/socket"
	"minos/internal/timeline"
	"minos/internal/vfs"
)

// Server is one running instance: the URL a client dials, and the store behind it.
type Server struct {
	Base  string
	Store *timeline.Timeline

	settings     config.Config
	historyLimit int
	stop         func()
}

// Start launches a server with its state under the test's temporary directory,
// stopped when the test ends. historyLimit caps a backfill, as config.HistoryLimit does.
func Start(t testing.TB, historyLimit int) *Server {
	t.Helper()
	root := t.TempDir()

	settings := config.Load()
	settings.Dist = filepath.Join(root, "dist")
	settings.VfsRoot = filepath.Join(root, "vfs")
	settings.RunDir = filepath.Join(root, "run")
	for _, directory := range []string{settings.Dist, settings.VfsRoot, settings.RunDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("cannot create %s: %v", directory, err)
		}
	}
	if err := os.WriteFile(filepath.Join(settings.Dist, "index.html"), []byte("<html>minos</html>"), 0o644); err != nil {
		t.Fatalf("cannot write the index: %v", err)
	}

	s := &Server{settings: settings, historyLimit: historyLimit}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	s.start(t, listener)
	t.Cleanup(func() { s.stop() })
	return s
}

// Restart stops the server and starts it again on the same address and the
// same database, as a process restart would: every connection is dropped and
// every session forgotten.
func (s *Server) Restart(t testing.TB) {
	t.Helper()
	address := strings.TrimPrefix(s.Base, "http://")
	s.stop()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("cannot listen on %s again: %v", address, err)
	}
	s.start(t, listener)
}

func (s *Server) start(t testing.TB, listener net.Listener) {
	t.Helper()
	settings := s.settings
	store, err := timeline.Open(filepath.Join(settings.RunDir, "timeline.db"), s.historyLimit, settings.Grace, settings.Unentered)
	if err != nil {
		t.Fatalf("cannot open the timeline: %v", err)
	}
	registry := socket.NewRegistry()
	handler := chat.New(store, registry, settings)
	if err := handler.EnsureSystemChannel(); err != nil {
		t.Fatalf("cannot declare the system channel: %v", err)
	}
	handler.StartSweeper()

	api := httpapi.New(settings, vfs.New(settings), registry, handler)
	server := httptest.NewUnstartedServer(api.Handler())
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	s.Base, s.Store = server.URL, store
	s.stop = func() {
		api.Close()
		server.CloseClientConnections()
		server.Close()
		handler.Close()
		store.Close()
	}
}

// SocketPath is a path to bind a unix socket at, removed when the test ends.
// Not under t.TempDir(): on macOS that path can pass the 104-byte bind limit.
func SocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "minos-")
	if err != nil {
		t.Fatalf("cannot create a socket directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "run.sock")
}
