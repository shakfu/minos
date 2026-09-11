// Package testserver runs a whole minos server in process, for tests that need a
// real HTTP server and a real websocket rather than a mock of either.
package testserver

import (
	"net/http/httptest"
	"os"
	"path/filepath"
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

	store, err := timeline.Open(filepath.Join(settings.RunDir, "timeline.db"), historyLimit, settings.Grace)
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
	server := httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		api.Close()
		server.Close()
		handler.Close()
		store.Close()
	})
	return &Server{Base: server.URL, Store: store}
}
