// Command minosb is one run's broker.
//
// It holds the credential, the conversation and the run's socket, and it is a
// child of the dispatcher: its death ends the run. The dispatcher keeps the
// container handle for itself, so stopping a run never goes through here.
//
// Control output is one JSON object per line on stdout, or in the file -events
// names, for the dispatcher to read: `ready` once the socket is answering, `pushed` for each frame on the agent's stdin, `held` and `turn` as the push
// lane fills and drains, `interrupt` with each request's outcome, `torn` when
// delivery gives up, `unseated` when the run lost its place in the room and
// could not take it back, `exited` when the run command ends, `stopped` on the way
// out, with reason `ended` when the agent exited after an `/end` from the
// room. Anything a person should read goes to stderr.
//
// With -events, stdout carries the agent's own stream instead, byte for byte.
// A dispatcher that reads the agent's output, as pma reads cost and progress
// from its worker's log, then sees what it would see without the broker.
//
// Control input is one JSON object per line on stdin, read only when there is
// a run command: `{"op":"interrupt","id":"..."}` stops the current turn. A
// line that is not one is answered with `refused`. End of input ends nothing.
//
// Everything after `--` is the run command, which is `sanduk run` in a real
// dispatch. With -agent-spec it is a one-shot CLI, run once per turn
// (docs/one-shot-agents.md). With -agent acp it speaks the Agent Client
// Protocol for the whole run (docs/acp.md). -task writes the agent's task as its first stdin frame, so the run
// command needs no wrapper that does it. Without one, the agent waits for the
// room: a person-driven run, which a person ends with `/end`. -linger closes the agent's stdin once
// it has been idle that long, so an agent that reads to end of input exits and
// the run ends without a signal. The broker owns its stdin and stdout: stdin is the only lane that
// reaches a model that never calls the shim, and stdout is the broker's only
// view of the work. It does not own the container. Stopping a run goes to the
// engine directly, so it never goes through this process; stopping a turn is
// a frame on the agent's stdin, so it always does.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"minos/internal/broker"
	"minos/internal/link"
)

// How often the tear and seat checks run. A tear is detected on the push
// goroutine and reported here, because the dispatcher must hear it even if
// the container never calls `messages` again.
const tearInterval = time.Second

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fallback := os.Getenv("MINOS_SERVER")
	if fallback == "" {
		fallback = "http://127.0.0.1:8000"
	}
	flags := flag.NewFlagSet("minosb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", fallback, "base URL of the minos server")
	user := flags.String("user", os.Getenv("MINOS_USER"), "the account this run speaks as")
	room := flags.String("room", "", "the room carrying the conversation")
	channel := flags.String("channel", "", "the channel carrying submissions; without one, submit is refused")
	window := flags.String("window", "all", "what this run may read, reported by status")
	socket := flags.String("socket", "", "the unix socket the container reaches, outside the work mount")
	mode := flags.Uint("mode", 0o600, "the socket's file mode; the socket is the credential")
	agent := flags.String("agent", "claude", "whose stream the run command speaks: claude, or acp for an Agent Client Protocol agent")
	agentSpec := flags.String("agent-spec", "",
		"a one-shot CLI's spec file: the run command runs once per turn, with {text} and {resume}")
	interrupters := flags.String("interrupters", "", "comma-separated users whose /interrupt in the room stops a turn")
	eventsPath := flags.String("events", "", "append control events to this file; stdout then carries the agent's stream")
	task := flags.String("task", "", "the agent's task, written as its first stdin frame")
	linger := flags.Duration("linger", 0, "close the agent's stdin after this long idle between turns; 0 never closes it")
	permission := flags.String("permission", "deny", "how an ACP agent's permission requests are answered: deny or allow")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// The password is read from the environment alone. An argument is visible
	// in the process list to every account on the host.
	password := os.Getenv("MINOS_PASSWORD")
	switch {
	case *socket == "":
		return fail(stderr, "minosb needs -socket: the path the container reaches")
	case *room == "":
		return fail(stderr, "minosb needs -room")
	case *user == "":
		return fail(stderr, "minosb needs -user, or MINOS_USER")
	case password == "":
		return fail(stderr, "minosb needs MINOS_PASSWORD in the environment")
	case *task != "" && flags.NArg() == 0:
		return fail(stderr, "-task needs a run command after --")
	case *permission != "deny" && *permission != "allow":
		return fail(stderr, "-permission is deny or allow, not %q", *permission)
	case *linger < 0:
		return fail(stderr, "-linger cannot be negative")
	case *linger > 0 && flags.NArg() == 0:
		return fail(stderr, "-linger needs a run command after --")
	}
	var spec *broker.Spec
	if *agentSpec != "" {
		loaded, err := broker.LoadSpec(*agentSpec)
		if err != nil {
			return fail(stderr, "cannot use -agent-spec: %v", err)
		}
		if flags.NArg() == 0 {
			return fail(stderr, "-agent-spec needs a run command after --")
		}
		spec = &loaded
	}
	if err := link.CheckPath(*socket); err != nil {
		return fail(stderr, "cannot use -socket %s: %v", *socket, err)
	}

	// Control events and the agent's stream never share stdout: interleaved,
	// either could split a line of the other.
	events, passthrough := stdout, io.Writer(nil)
	if *eventsPath != "" {
		file, err := os.OpenFile(*eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return fail(stderr, "cannot use -events %s: %v", *eventsPath, err)
		}
		defer file.Close()
		events, passthrough = file, stdout
	}
	report := reporter(events)

	run, err := broker.Open(broker.Config{
		Server: *server, User: *user, Password: password,
		Room: *room, Channel: *channel, Window: *window,
		Interrupters: names(*interrupters),
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	defer run.Stop()
	// The room is told the run's state as the dispatcher is.
	report = run.Watch(report)

	listener, err := link.Listen(*socket, os.FileMode(*mode))
	if err != nil {
		return fail(stderr, "cannot create %s: %v", *socket, err)
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
	var command process
	var relay *broker.Relay
	if argv := flags.Args(); len(argv) > 0 {
		var drained <-chan struct{}
		var err error
		switch {
		case spec != nil:
			command, relay, drained, err = startOneShot(run, *spec, argv, *task, *linger, report, passthrough)
		case *agent == "acp":
			command, relay, drained, err = startACP(run, argv, *permission == "allow", *task, *linger, report, passthrough)
		default:
			command, relay, drained, err = startStream(run, *agent, argv, *task, *linger, report, passthrough)
		}
		if err != nil {
			return fail(stderr, "%v", err)
		}
		go control(os.Stdin, relay.Interrupt, report)
		go func() {
			<-drained
			done <- command.Wait()
		}()
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
			reason := "exited"
			if relay.Ended() != "" {
				reason = "ended"
			}
			report(map[string]any{"event": "stopped", "reason": reason})
			return code
		case <-ticker.C:
			if torn, missing := run.Torn(); torn {
				report(map[string]any{"event": "torn", "missing": missing})
				report(map[string]any{"event": "stopped", "reason": "torn"})
				return stop(command, 1)
			}
			if !run.Seated() {
				report(map[string]any{"event": "unseated", "room": *room})
				report(map[string]any{"event": "stopped", "reason": "unseated"})
				return stop(command, 1)
			}
		}
	}
}

// process is the run command, as the main loop waits on and stops it.
type process interface {
	// Wait is its exit status, once its output is relayed.
	Wait() int
	// Kill stops it and claims nothing more: the container it started is
	// the dispatcher's to delete.
	Kill()
}

// streamed is a stream agent: one process for the whole run.
type streamed struct{ *exec.Cmd }

func (s streamed) Wait() int { return wait(s.Cmd) }

func (s streamed) Kill() {
	if s.Process != nil {
		_ = s.Process.Kill()
	}
}

// startStream runs a stream agent with its stdio on the broker's pipes. A
// non-nil passthrough gets every byte of its stdout as it is read.
func startStream(run *broker.Broker, agent string, argv []string, task string, linger time.Duration,
	report func(map[string]any), passthrough io.Writer) (process, *broker.Relay, <-chan struct{}, error) {
	adapter, err := broker.AdapterFor(agent)
	if err != nil {
		return nil, nil, nil, err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Env = childEnv(os.Environ())
	// The run command's own notes are a person's to read, not the room's.
	command.Stderr = os.Stderr

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := command.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("cannot start %s: %w", argv[0], err)
	}
	var stream io.Reader = stdout
	if passthrough != nil {
		stream = io.TeeReader(stdout, passthrough)
	}
	return relayed(run, adapter, streamed{command}, stdin, stream, task, linger, report)
}

// startOneShot runs a one-shot agent once per turn. Its passthrough is the
// CLIs' own stdout; the relay reads the turn lines OneShot writes apart.
func startOneShot(run *broker.Broker, spec broker.Spec, argv []string, task string, linger time.Duration,
	report func(map[string]any), passthrough io.Writer) (process, *broker.Relay, <-chan struct{}, error) {
	agent, err := broker.NewOneShot(spec, argv, childEnv(os.Environ()), os.Stderr, passthrough)
	if err != nil {
		return nil, nil, nil, err
	}
	agent.Start()
	return relayed(run, agent, agent, agent.Stdin, agent.Stdout, task, linger, report)
}

// startACP runs an ACP agent for the run, in a session at the broker's own
// working directory. Its passthrough is the agent's ACP messages.
func startACP(run *broker.Broker, argv []string, allow bool, task string, linger time.Duration,
	report func(map[string]any), passthrough io.Writer) (process, *broker.Relay, <-chan struct{}, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, err
	}
	agent, err := broker.NewACP(argv, childEnv(os.Environ()), cwd, os.Stderr, passthrough, report, allow)
	if err != nil {
		return nil, nil, nil, err
	}
	agent.Start()
	return relayed(run, agent, agent, agent.Stdin, agent.Stdout, task, linger, report)
}

// relayed puts a started agent behind the relay. The channel closes once its
// stdout is read and relayed; Wait closes the pipe, so it must not run before
// then or the last turn is lost. A non-empty task is the first frame on stdin.
// A positive linger closes stdin once the agent idles that long.
func relayed(run *broker.Broker, adapter broker.Adapter, command process, stdin io.WriteCloser, stdout io.Reader,
	task string, linger time.Duration, report func(map[string]any)) (process, *broker.Relay, <-chan struct{}, error) {
	// Before Deliver starts, so nothing reaches stdin ahead of the task. The
	// relay starts inside a turn, which this frame begins.
	if task != "" {
		frame, err := adapter.Push([]string{task})
		if err == nil {
			_, err = stdin.Write(frame)
		}
		if err != nil {
			command.Kill()
			_ = stdin.Close()
			command.Wait()
			return nil, nil, nil, fmt.Errorf("cannot give the agent its task: %w", err)
		}
		report(map[string]any{"event": "pushed", "messages": 1})
	}

	relay := broker.NewRelay(run, adapter, stdin, report)
	if task == "" {
		relay.Untasked()
	}
	relay.Linger(linger)
	go relay.Deliver()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		if err := relay.Read(stdout); err != nil {
			report(map[string]any{"event": "unread", "error": err.Error()})
		}
	}()
	return command, relay, drained, nil
}

// control reads the dispatcher's requests until its input ends. The ids are
// the dispatcher's own, echoed in the `interrupt` event.
func control(input io.Reader, interrupt func(broker.Ask), report func(map[string]any)) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request struct {
			Op string `json:"op"`
			ID string `json:"id"`
		}
		var refusal string
		switch err := json.Unmarshal([]byte(line), &request); {
		case err != nil:
			refusal = "not a JSON object: " + err.Error()
		case request.Op != "interrupt":
			refusal = fmt.Sprintf("no op %q; there is: interrupt", request.Op)
		case request.ID == "":
			refusal = "an interrupt needs an id"
		default:
			interrupt(broker.Ask{By: "dispatcher", ID: request.ID})
			continue
		}
		report(map[string]any{"event": "refused", "error": refusal})
	}
	if err := scanner.Err(); err != nil {
		report(map[string]any{"event": "refused", "error": "control input: " + err.Error()})
	}
}

// names splits a comma-separated list, dropping blanks.
func names(list string) []string {
	var found []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			found = append(found, name)
		}
	}
	return found
}

// childEnv is the broker's environment without the credential. A nil Env
// would hand the run command the password only the broker may hold.
func childEnv(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return name == "MINOS_PASSWORD" || name == "MINOS_USER"
	})
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
func stop(command process, code int) int {
	if command != nil {
		command.Kill()
	}
	return code
}

// reporter writes control events to w, one line each. The lock keeps lines
// whole: events come from the relay, the control lane and the main loop.
// `stopped` is the last line: the relay can still be reporting a turn while
// the main loop exits, and a dispatcher stops reading at `stopped`.
func reporter(w io.Writer) func(map[string]any) {
	var mutex sync.Mutex
	stopped := false
	return func(event map[string]any) {
		raw, err := json.Marshal(event)
		if err != nil {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		if stopped {
			return
		}
		stopped = event["event"] == "stopped"
		fmt.Fprintln(w, string(raw))
	}
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "minosb: "+format+"\n", args...)
	return 2
}
