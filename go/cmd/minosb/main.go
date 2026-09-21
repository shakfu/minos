// Command minosb is one run's broker.
//
// It holds the credential, the conversation and the run's socket, and it is a
// child of the dispatcher: its death ends the run. The dispatcher keeps the
// container handle for itself, so stopping a run never goes through here.
//
// Control output is one JSON object per line on stdout, for the dispatcher to
// read: `ready` once the socket is answering, `held` and `turn` as the push
// lane fills and drains, `torn` when delivery gives up, `exited` when the run
// command ends, `stopped` on the way out. Anything a person should read goes
// to stderr.
//
// Everything after `--` is the run command, which is `sanduk run` in a real
// dispatch. The broker owns its stdin and stdout: stdin is the only lane that
// reaches a model that never calls the shim, and stdout is the broker's only
// view of the work. It does not own the container. Stopping a run goes to the
// engine directly, so it never goes through this process.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"minos/internal/broker"
	"minos/internal/link"
)

// How often the tear check runs. A tear is detected on the push goroutine and
// reported here, because the dispatcher must hear it even if the container
// never calls `messages` again.
const tearInterval = time.Second

func main() { os.Exit(run()) }

func run() int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	server := flag.String("server", fallback, "base URL of the minos server")
	user := flag.String("user", os.Getenv("MINOS_USER"), "the account this run speaks as")
	room := flag.String("room", "", "the room carrying the conversation")
	channel := flag.String("channel", "", "the channel carrying submissions; without one, submit is refused")
	window := flag.String("window", "all", "what this run may read, reported by status")
	socket := flag.String("socket", "", "the unix socket the container reaches, outside the work mount")
	mode := flag.Uint("mode", 0o600, "the socket's file mode; the socket is the credential")
	agent := flag.String("agent", "claude", "whose stream the run command speaks")
	flag.Parse()

	// The password is read from the environment alone. An argument is visible
	// in the process list to every account on the host.
	password := os.Getenv("MINOS_PASSWORD")
	switch {
	case *socket == "":
		return fail("minosb needs -socket: the path the container reaches")
	case *room == "":
		return fail("minosb needs -room")
	case *user == "":
		return fail("minosb needs -user, or MINOS_USER")
	case password == "":
		return fail("minosb needs MINOS_PASSWORD in the environment")
	}

	run, err := broker.Open(broker.Config{
		Server: *server, User: *user, Password: password,
		Room: *room, Channel: *channel, Window: *window,
	})
	if err != nil {
		return fail("%v", err)
	}
	defer run.Stop()

	listener, err := link.Listen(*socket, os.FileMode(*mode))
	if err != nil {
		return fail("cannot create %s: %v", *socket, err)
	}
	defer func() {
		listener.Close()
		_ = os.Remove(*socket)
	}()
	go link.Serve(listener, run.Handle)

	report(map[string]any{"event": "ready", "room": *room, "channel": *channel, "socket": *socket})

	// The run command is optional: a broker with none answers the socket and
	// nothing else, which is what a dispatcher that starts the agent itself
	// wants. With one, the broker owns its pipes.
	done := make(chan int, 1)
	var command *exec.Cmd
	if argv := flag.Args(); len(argv) > 0 {
		adapter, err := broker.AdapterFor(*agent)
		if err != nil {
			return fail("%v", err)
		}
		if command, err = start(run, adapter, argv); err != nil {
			return fail("%v", err)
		}
		go func() { done <- wait(command) }()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(tearInterval)
	defer ticker.Stop()

	for {
		select {
		case <-signals:
			report(map[string]any{"event": "stopped", "reason": "signal"})
			return stop(command, 0)
		case code := <-done:
			report(map[string]any{"event": "exited", "code": code})
			report(map[string]any{"event": "stopped", "reason": "exited"})
			return code
		case <-ticker.C:
			if torn, missing := run.Torn(); torn {
				report(map[string]any{"event": "torn", "missing": missing})
				report(map[string]any{"event": "stopped", "reason": "torn"})
				return stop(command, 1)
			}
		}
	}
}

// start runs the agent with its stdio on the broker's pipes.
func start(run *broker.Broker, adapter broker.Adapter, argv []string) (*exec.Cmd, error) {
	command := exec.Command(argv[0], argv[1:]...)
	// The run command's own notes are a person's to read, not the room's.
	command.Stderr = os.Stderr

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", argv[0], err)
	}

	relay := broker.NewRelay(run, adapter, stdin, report)
	go relay.Deliver()
	go func() {
		if err := relay.Read(stdout); err != nil {
			report(map[string]any{"event": "unread", "error": err.Error()})
		}
	}()
	return command, nil
}

// wait reports the run command's exit the way a shell does.
func wait(command *exec.Cmd) int {
	err := command.Wait()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	default:
		return 1
	}
}

// stop ends the run command. The container it started is the dispatcher's to
// delete: this kills what this process spawned and claims nothing more.
func stop(command *exec.Cmd, code int) int {
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
	return code
}

func report(event map[string]any) {
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, string(raw))
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "minosb: "+format+"\n", args...)
	return 2
}
