// Command minosg serves one worker's room as an A2A agent.
//
// It is a minos client on the host, like the terminal client, and an A2A
// server for an orchestrator that speaks A2A. See internal/gateway for the
// mapping and docs/dev/agent2agent.md for the decision.
//
// The listener must be a loopback address: the bearer token travels in clear,
// and the link carries no TLS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minos/internal/gateway"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	flags := flag.NewFlagSet("minosg", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", fallback, "base URL of the minos server")
	user := flags.String("user", os.Getenv("MINOS_USER"), "the account the gateway speaks as")
	room := flags.String("room", "", "the worker's room")
	worker := flags.String("worker", "", "the account whose messages answer a task")
	listen := flags.String("listen", "127.0.0.1:8100", "the loopback address A2A clients reach")
	timeout := flags.Duration("timeout", gateway.DefaultTimeout, "how long a task waits for the worker")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Secrets come from the environment alone. An argument is visible in the
	// process list to every account on the host.
	password := os.Getenv("MINOS_PASSWORD")
	token := os.Getenv("MINOS_A2A_TOKEN")
	switch {
	case *room == "":
		return fail(stderr, "minosg needs -room")
	case *worker == "":
		return fail(stderr, "minosg needs -worker")
	case *user == "":
		return fail(stderr, "minosg needs -user, or MINOS_USER")
	case password == "":
		return fail(stderr, "minosg needs MINOS_PASSWORD in the environment")
	case token == "":
		return fail(stderr, "minosg needs MINOS_A2A_TOKEN in the environment")
	}
	if err := loopback(*listen); err != nil {
		return fail(stderr, "cannot use -listen %s: %v", *listen, err)
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	g, err := gateway.Open(gateway.Config{
		Server: *server, User: *user, Password: password,
		Room: *room, Worker: *worker,
		Base: "http://" + listener.Addr().String(), Token: token, Timeout: *timeout,
	})
	if err != nil {
		listener.Close()
		return fail(stderr, "%v", err)
	}
	defer g.Stop()

	httpServer := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	fmt.Fprintf(stderr, "minosg: serving room %s on http://%s\n", *room, listener.Addr())

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		return 0
	case err := <-served:
		return fail(stderr, "%v", err)
	}
}

// loopback refuses an address another host could reach.
func loopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("not a loopback address; the token would travel in clear")
	}
	return nil
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "minosg: "+format+"\n", args...)
	return 2
}
