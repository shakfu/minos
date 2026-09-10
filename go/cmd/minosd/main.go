// Command minosd is the minos server.
//
// One process, a goroutine per connection, and an in-process fan-out. What the
// Python server needed a message bus for -- several worker processes, because
// CPython cannot use several cores in one -- has no counterpart here, so the
// broker, the worker leases, the liveness locks and the propagation delays are
// all absent rather than ported.
//
// What it speaks is docs/wire-contract.md, and what says so is
// tests/conformance:
//
//	make conformance-go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"minos/internal/chat"
	"minos/internal/config"
	"minos/internal/httpapi"
	"minos/internal/socket"
	"minos/internal/timeline"
	"minos/internal/vfs"
)

// How long a shutdown waits for connections to finish before giving up.
const shutdownGrace = 5 * time.Second

func main() {
	log.SetFlags(log.LstdFlags)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	settings := config.Load()

	for _, directory := range []string{settings.VfsRoot, settings.RunDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", directory, err)
		}
	}

	// A missing browser build is not fatal: the terminal client needs the API
	// and the websocket, not a bundle. Anyone expecting a page still gets told
	// why they will not get one.
	if _, err := os.Stat(filepath.Join(settings.Dist, "index.html")); err != nil {
		log.Printf("No client build in %s; serving the API only.", settings.Dist)
	}

	store, err := timeline.Open(settings.DatabasePath(), config.HistoryLimit, settings.Grace)
	if err != nil {
		return fmt.Errorf("cannot open the timeline: %w", err)
	}
	defer store.Close()

	registry := socket.NewRegistry()
	handler := chat.New(store, registry, settings)
	if err := handler.EnsureSystemChannel(); err != nil {
		return fmt.Errorf("cannot declare the system channel: %w", err)
	}

	// Transient rooms are deleted a grace period after their last occupant
	// leaves, and that has to happen whether or not anyone is connected -- it is
	// a promise to the people who spoke in one, not a housekeeping convenience.
	handler.StartSweeper()
	defer handler.Close()

	api := httpapi.New(settings, vfs.New(settings), registry, handler)
	defer api.Close()

	address := net.JoinHostPort(settings.Host, fmt.Sprint(settings.Port))
	server := &http.Server{
		Addr:    address,
		Handler: api.Handler(),

		// No write timeout: a websocket lives as long as its client does, and
		// the transport enforces its own deadlines per frame.
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", address, err)
	}
	log.Printf("minos listening on http://%s", listener.Addr())

	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stopping:
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return server.Shutdown(ctx)
	}
}
