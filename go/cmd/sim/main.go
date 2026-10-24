// Command sim runs three scripted agents through the real minos stack: one
// go/minosd, and per agent a go/minosb, its socket, and go/minosa. The agents
// share one room, stream progress into it, read each other's findings, and
// submit proposals to a moderated channel. Run it with `make sim`.
//
// The agents are alice, bob and worker, so each sees the others: a broker
// drops its own account's messages, and every phase 0 run is otherwise
// `worker`. Three runs in one room break design.md D10, so the server of its
// own counts all three as agents and admits three per room. A server given
// with -server counts only what its MINOS_AGENTS lists. demo founds the room and the channel and moderates. Without
// -manual a scripted moderator approves two proposals and rejects one; with
// it, the simulation waits for a person to decide them in the terminal client.
//
// To watch from the terminal client, run against a `make serve` with -server,
// slowed with -pace. Each run founds its own room and channel, titled with
// the time.
//
// No model runs. The agent is this binary again, selected by SIM_AGENT.
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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"minos/conformance"
	"minos/internal/client"
)

const (
	agentVariable = "SIM_AGENT"
	awaitVariable = "SIM_AWAIT"
	paceVariable  = "SIM_PACE"
)

var agents = []string{"alice", "bob", "worker"}

// The scripted moderator's decisions, by author.
var verdicts = map[string]struct {
	approve bool
	comment string
}{
	"alice":  {approve: true},
	"bob":    {comment: "not without a load test at 10x traffic"},
	"worker": {approve: true},
}

// How long the scripted moderator takes to decide.
const deliberation = time.Second

// An agent idle this long between turns has its input closed, and exits.
const linger = 2 * time.Second

func main() {
	if name := os.Getenv(agentVariable); name != "" {
		os.Exit(agent(name))
	}
	var o options
	flag.BoolVar(&o.manual, "manual", false, "leave the decisions to a person in the terminal client")
	flag.Float64Var(&o.pace, "pace", 1, "multiply every scripted delay by this; 3 is slow enough to watch")
	server := flag.String("server", "", "run against this server, such as a `make serve`; default a server of its own")
	flag.Parse()
	if o.pace <= 0 {
		fmt.Fprintln(os.Stderr, "sim: -pace must be positive")
		os.Exit(2)
	}
	if err := run(*server, o); err != nil {
		fmt.Fprintf(os.Stderr, "sim: %v\n", err)
		os.Exit(1)
	}
}

// options is what the flags choose.
type options struct {
	manual bool
	pace   float64
}

// scaled is d at the chosen pace; a zero pace is 1.
func (o options) scaled(d time.Duration) time.Duration {
	if o.pace <= 0 {
		return d
	}
	return time.Duration(float64(d) * o.pace)
}

// agentSettings count every agent as one and admit them all to one room,
// which D10 refuses by default.
func agentSettings() map[string]string {
	return map[string]string{
		"MINOS_AGENTS":      strings.Join(agents, ","),
		"MINOS_ROOM_AGENTS": fmt.Sprint(len(agents)),
	}
}

// run simulates against base, or against the built go/minosd on a database
// of its own when base is empty.
func run(base string, o options) error {
	_, source, _, _ := runtime.Caller(0)
	bin := filepath.Join(filepath.Dir(source), "..", "..")
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if base != "" {
		return simulate(base, bin, self, o, os.Stdout)
	}
	state, err := os.MkdirTemp("", "minos-sim-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	server, err := conformance.Launch(state, []string{filepath.Join(bin, "minosd")}, agentSettings())
	if err != nil {
		return err
	}
	defer server.Stop()
	return simulate(server.Base, bin, self, o, os.Stdout)
}

// narrator prints one line per thing that happened, in the order seen.
type narrator struct {
	mutex sync.Mutex
	out   io.Writer
	start time.Time
}

func (n *narrator) say(lane, format string, args ...any) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	fmt.Fprintf(n.out, "%5.1fs  %-13s %s\n", time.Since(n.start).Seconds(), lane, fmt.Sprintf(format, args...))
}

// simulate runs the review against the server at base. bin holds minosb and
// minosa; self is the binary that plays the agent. It returns once every
// agent has exited, with an error naming each check that failed.
func simulate(base, bin, self string, o options, out io.Writer) error {
	n := &narrator{out: out, start: time.Now()}
	_, demo, _, err := client.Connect(base, "demo", "demo")
	if err != nil {
		return fmt.Errorf("demo cannot connect: %w", err)
	}
	defer demo.Stop()

	invite := make([]client.Principal, 0, len(agents))
	for _, name := range agents {
		invite = append(invite, client.Principal{Kind: "user", ID: name})
	}
	// Channel titles are unique, and a shared server keeps every earlier run's.
	stamp := time.Now().Format("15:04:05")
	room, err := demo.OpenRoom(invite, "Review of #42 "+stamp, "persisted")
	if err != nil {
		return fmt.Errorf("cannot open the room: %w", err)
	}
	channel, err := demo.CreateChannel("Decisions "+stamp, nil, "")
	if err != nil {
		return fmt.Errorf("cannot found the channel: %w", err)
	}
	if _, err := demo.Appoint(channel.ID, "demo"); err != nil {
		return fmt.Errorf("cannot appoint demo: %w", err)
	}
	// So the channel is listed in demo's terminal client too.
	if _, ok := demo.Space(channel.ID); !ok {
		if _, err := demo.Subscribe(channel.ID); err != nil {
			return fmt.Errorf("cannot subscribe demo: %w", err)
		}
	}
	n.say("setup", "room %q %s with %s; channel %q %s, moderated by demo",
		room.Title, short(room.ID), strings.Join(agents, ", "), channel.Title, short(channel.ID))

	await := "30"
	if o.manual {
		await = "3600"
		n.say("setup", "decide from another shell:")
		n.say("setup", "  MINOS_SERVER=%s ./go/minos -user demo -password demo", base)
		n.say("setup", "  enter %q, then /queue, /approve <id>, /reject <id> <why>", channel.Title)
	}

	done := make(chan struct{})
	var watchers sync.WaitGroup
	watchers.Add(2)
	go func() { defer watchers.Done(); watchRoom(n, demo, room.ID, done) }()
	decided := make(chan string, len(agents))
	go func() { defer watchers.Done(); moderate(n, demo, channel.ID, o, decided, done) }()

	runs := make([]*exec.Cmd, 0, len(agents))
	var streams sync.WaitGroup
	sockets := make([]string, 0, len(agents))
	defer func() {
		for _, socket := range sockets {
			_ = os.Remove(socket)
		}
	}()
	for _, name := range agents {
		// Not under a test's temporary directory: socket paths are limited
		// to 103 bytes.
		socket := filepath.Join(os.TempDir(), fmt.Sprintf("minos-sim-%d-%s.sock", os.Getpid(), name))
		sockets = append(sockets, socket)
		command := exec.Command(filepath.Join(bin, "minosb"),
			"-server", base, "-user", name, "-room", room.ID, "-channel", channel.ID,
			"-socket", socket, "-linger", linger.String(), "-task", roles[name].task, "--", self)
		command.Env = append(os.Environ(),
			"MINOS_PASSWORD="+name, "MINOS_SOCKET="+socket, agentVariable+"="+name, awaitVariable+"="+await,
			fmt.Sprintf("%s=%g", paceVariable, o.pace),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		stdout, err := command.StdoutPipe()
		if err != nil {
			return err
		}
		stderr, err := command.StderrPipe()
		if err != nil {
			return err
		}
		if err := command.Start(); err != nil {
			return fmt.Errorf("cannot start %s's broker: %w", name, err)
		}
		runs = append(runs, command)
		streams.Add(2)
		go func() { defer streams.Done(); events(n, name, stdout) }()
		go func() { defer streams.Done(); lines(stderr, func(line string) { n.say(name, "%s", line) }) }()
	}

	// A signal stops the brokers, which stop their agents, so the deferred
	// cleanup here and in run still happens.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	interrupted := make(chan struct{})
	go func() {
		select {
		case <-signals:
			close(interrupted)
			for _, command := range runs {
				_ = command.Process.Signal(syscall.SIGTERM)
			}
		case <-done:
		}
	}()

	// Wait reads nothing more once it returns, so the pipes are drained first.
	streams.Wait()
	var failures []string
	for i, command := range runs {
		if err := command.Wait(); err != nil {
			failures = append(failures, fmt.Sprintf("%s's run ended: %v", agents[i], err))
		}
	}
	close(done)
	watchers.Wait()
	select {
	case <-interrupted:
		return errors.New("interrupted")
	default:
	}

	if len(decided) != len(agents) {
		failures = append(failures, fmt.Sprintf("%d of %d proposals were decided", len(decided), len(agents)))
	}
	log := demo.Log(room.ID)
	for _, name := range agents {
		if !slices.ContainsFunc(log, func(m client.Message) bool {
			return m.Author == name && m.Body == roles[name].summary
		}) {
			failures = append(failures, name+"'s summary was not relayed to the room")
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	n.say("done", "%d agents exited 0, %d proposals decided, %d messages in the room", len(agents), len(decided), len(log))
	return nil
}

// watchRoom prints what the room says, as demo's client receives it.
func watchRoom(n *narrator, demo *client.Client, room string, done <-chan struct{}) {
	var seen int64
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, message := range demo.Log(room) {
			if message.Seq > seen {
				seen = message.Seq
				n.say("room", "%s: %s", message.Author, oneLine(message.Body))
			}
		}
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}

// moderate reports each submission as it is queued and, unless manual,
// decides it. Each id is sent on decided once it is no longer queued.
func moderate(n *narrator, demo *client.Client, channel string, o options, decided chan<- string, done <-chan struct{}) {
	queued := map[string]bool{}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		pending, err := demo.Queue(channel)
		if err != nil {
			n.say("moderator", "cannot read the queue: %v", err)
			continue
		}
		for id := range queued {
			if !slices.ContainsFunc(pending, func(s client.Submission) bool { return s.ID == id }) {
				delete(queued, id)
				decided <- id
			}
		}
		for _, submission := range pending {
			if _, known := queued[submission.ID]; known {
				continue
			}
			queued[submission.ID] = true
			n.say("queue", "%s %s submits: %s", short(submission.ID), submission.Author, submission.Subject)
			if o.manual {
				continue
			}
			go decide(n, demo, submission, o.scaled(deliberation))
		}
	}
}

func decide(n *narrator, demo *client.Client, submission client.Submission, deliberation time.Duration) {
	time.Sleep(deliberation)
	verdict := verdicts[submission.Author]
	if verdict.approve {
		if err := demo.Approve(submission.ID); err != nil {
			n.say("moderator", "cannot approve %s: %v", short(submission.ID), err)
			return
		}
		n.say("moderator", "demo approves %s", short(submission.ID))
		return
	}
	if err := demo.Reject(submission.ID, verdict.comment); err != nil {
		n.say("moderator", "cannot reject %s: %v", short(submission.ID), err)
		return
	}
	n.say("moderator", "demo rejects %s: %s", short(submission.ID), verdict.comment)
}

// events prints the broker's control events that show the push lane. Each
// `held` is left out: the `turn` after it counts them.
func events(n *narrator, name string, stdout io.Reader) {
	lines(stdout, func(line string) {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			n.say(name+"/event", "%s", line)
			return
		}
		switch event["event"] {
		case "ready":
			n.say(name+"/event", "ready on %s", event["socket"])
		case "turn":
			if held, _ := event["delivering"].(float64); held > 0 {
				n.say(name+"/event", "turn ended; pushing the %v messages held during it as one frame", held)
			}
		case "closed":
			n.say(name+"/event", "idle %s, input closed", event["linger"])
		case "exited":
			n.say(name+"/event", "exited %v", event["code"])
		}
	})
}

func lines(r io.Reader, each func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		each(scanner.Text())
	}
}

func short(id string) string { return id[:min(8, len(id))] }
