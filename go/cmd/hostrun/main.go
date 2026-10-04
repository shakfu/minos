// Command hostrun drives one real agent under go/minosb, on the host, with no
// container. It checks what the scripted fakes in internal/broker cannot: that
// a real Claude Code process ends turns the way the relay assumes, that a
// message held mid-turn is answered after it, that both interrupt lanes stop a
// turn, and whether the agent calls the shim at all.
//
// It spends turns on the caller's Claude account, and the agent runs
// unsandboxed in a temporary directory. Never part of `make test`: a model is
// never in a test path. Run it with `make host-run`.
//
// HOSTRUN_AGENT replaces the agent command, which sh -c runs with the task
// as its first stdin frame. -step bounds each wait.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"minos/conformance"
	"minos/internal/client"
)

// Bash is allowed for minosa alone, so the shim is the only tool it can run.
const defaultAgent = `claude -p --input-format stream-json --output-format stream-json ` +
	`--verbose --allowedTools 'Bash(minosa:*)'`

// The agent gets its task as the first frame, then whatever minosb pushes.
const feed = `{ printf '%s\n' "$MINOS_TASK"; cat; } | exec `

const task = "You are a worker in a minos chat room. The `minosa` command is your " +
	"only way to speak in the room besides your replies, which are relayed there. " +
	"Run `minosa say 'worker online'` once now. Then reply with exactly: READY. " +
	"Each later user message is from someone in the room; follow it."

// A turn long enough to still be running a few seconds after it starts.
const longTurn = "Count from 1 to 300, one number per line, and nothing else."

// How long after a long turn is pushed the probe acts inside it.
const midTurn = 4 * time.Second

func main() {
	step := flag.Duration("step", 3*time.Minute, "how long each step waits")
	out := flag.String("out", "", "where to write the transcript; default a temporary directory")
	flag.Parse()
	os.Exit(run(*step, *out))
}

type probe struct {
	step      time.Duration
	developer *client.Client
	room      string
	control   io.Writer

	mutex  sync.Mutex
	events []map[string]any
	checks []string
	failed int
}

func run(step time.Duration, out string) int {
	if out == "" {
		var err error
		if out, err = os.MkdirTemp("", "minos-hostrun-"); err != nil {
			return fail("%v", err)
		}
	}
	root := repoRoot()
	for _, binary := range []string{"minosd", "minosb", "minosa"} {
		if _, err := os.Stat(filepath.Join(root, "go", binary)); err != nil {
			return fail("go/%s is not built; run make go", binary)
		}
	}
	agent := os.Getenv("HOSTRUN_AGENT")
	if agent == "" {
		if _, err := exec.LookPath("claude"); err != nil {
			return fail("claude is not on PATH")
		}
		agent = defaultAgent
	}

	server, err := conformance.Launch(filepath.Join(out, "server"), []string{filepath.Join(root, "go", "minosd")}, nil)
	if err != nil {
		return fail("%v", err)
	}
	defer server.Stop()

	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		return fail("the developer cannot connect: %v", err)
	}
	defer developer.Stop()
	room, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "worker"}}, "Host run", "persisted")
	if err != nil {
		return fail("cannot open the room: %v", err)
	}

	work := filepath.Join(out, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fail("%v", err)
	}
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("minos-hostrun-%d.sock", os.Getpid()))
	frame, _ := json.Marshal(map[string]any{
		"type": "user", "message": map[string]any{"role": "user", "content": task},
	})
	broker := exec.Command(filepath.Join(root, "go", "minosb"),
		"-server", server.Base, "-user", "worker", "-room", room.ID,
		"-socket", socket, "-interrupters", "demo", "--", "sh", "-c", feed+agent)
	broker.Dir = work
	broker.Env = append(os.Environ(),
		"MINOS_PASSWORD=worker", "MINOS_SOCKET="+socket, "MINOS_TASK="+string(frame),
		"PATH="+filepath.Join(root, "go")+string(os.PathListSeparator)+os.Getenv("PATH"))
	stderr, err := os.Create(filepath.Join(out, "minosb.stderr"))
	if err != nil {
		return fail("%v", err)
	}
	defer stderr.Close()
	broker.Stderr = stderr
	control, err := broker.StdinPipe()
	if err != nil {
		return fail("%v", err)
	}
	events, err := broker.StdoutPipe()
	if err != nil {
		return fail("%v", err)
	}
	if err := broker.Start(); err != nil {
		return fail("cannot start minosb: %v", err)
	}

	p := &probe{step: step, developer: developer, room: room.ID, control: control}
	go p.read(events)
	p.script()

	_ = broker.Process.Signal(syscall.SIGTERM)
	waited := make(chan error, 1)
	go func() { waited <- broker.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		_ = broker.Process.Kill()
		<-waited
	}
	p.check("minosb reports stopped", p.has(func(e map[string]any) bool { return e["event"] == "stopped" }))

	p.write(out)
	fmt.Printf("\n%d checks, %d failed. Transcript in %s\n", len(p.checks), p.failed, out)
	if p.failed > 0 {
		return 1
	}
	return 0
}

// script is the run, one step after another. A step that times out is a
// failed check, and the next step still runs.
func (p *probe) script() {
	p.check("minosb is ready", p.wait(func(e map[string]any) bool { return e["event"] == "ready" }))

	// The task turn: the shim, then a relayed reply.
	p.check("the task turn is relayed", p.waitWorker("READY"))
	p.check("the agent called minosa say", p.said("worker online"))

	// A message during a turn is held, then answered in the next one.
	turns := p.count("turn")
	p.say(longTurn)
	time.Sleep(midTurn)
	p.say("When you have finished counting, reply with exactly: BANANA")
	p.check("a mid-turn message is held", p.wait(func(e map[string]any) bool { return e["event"] == "held" }))
	p.check("the held message is answered", p.waitWorker("BANANA"))
	p.check("a turn ended between them", p.count("turn") > turns)

	// The developer interrupts from the room, with a correction.
	p.say(longTurn)
	time.Sleep(midTurn)
	p.say("/interrupt Stop counting. Reply with exactly: CORRECTED")
	p.check("the room's interrupt is sent", p.wait(interrupt("demo", "sent")))
	p.check("the correction is answered", p.waitWorker("CORRECTED"))

	// The dispatcher interrupts on stdin, with nothing after it.
	turns = p.count("turn")
	p.say(longTurn)
	time.Sleep(midTurn)
	p.command(`{"op":"interrupt","id":"p1"}`)
	p.check("the dispatcher's interrupt is sent", p.wait(interrupt("dispatcher", "sent")))
	p.check("the interrupted turn ends", p.until(func() bool { return p.count("turn") > turns }))
	p.say("Reply with exactly: AFTER")
	p.check("the agent answers after a bare interrupt", p.waitWorker("AFTER"))

	// Between turns there is nothing to stop.
	p.command(`{"op":"interrupt","id":"p2"}`)
	p.check("an interrupt between turns is idle", p.wait(interrupt("dispatcher", "idle")))
	p.command(`{"op":"stop"}`)
	p.check("an unknown control op is refused", p.wait(func(e map[string]any) bool { return e["event"] == "refused" }))
}

func interrupt(by, outcome string) func(map[string]any) bool {
	return func(e map[string]any) bool {
		return e["event"] == "interrupt" && e["by"] == by && e["outcome"] == outcome
	}
}

func (p *probe) read(events io.Reader) {
	scanner := bufio.NewScanner(events)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			event = map[string]any{"event": "unparsed", "line": scanner.Text()}
		}
		event["t"] = time.Now().Format("15:04:05.000")
		fmt.Printf("  event %s\n", scanner.Text())
		p.mutex.Lock()
		p.events = append(p.events, event)
		p.mutex.Unlock()
	}
}

func (p *probe) say(body string) {
	fmt.Printf("  demo: %s\n", body)
	if err := p.developer.Send(p.room, body); err != nil {
		fmt.Printf("  cannot send: %v\n", err)
	}
}

func (p *probe) command(line string) {
	fmt.Printf("  stdin %s\n", line)
	if _, err := fmt.Fprintln(p.control, line); err != nil {
		fmt.Printf("  cannot write minosb's stdin: %v\n", err)
	}
}

func (p *probe) check(name string, ok bool) {
	mark := "PASS"
	if !ok {
		mark = "FAIL"
		p.failed++
	}
	line := mark + "  " + name
	fmt.Println(line)
	p.checks = append(p.checks, line)
}

func (p *probe) has(match func(map[string]any) bool) bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	for _, event := range p.events {
		if match(event) {
			return true
		}
	}
	return false
}

func (p *probe) count(name string) int {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	n := 0
	for _, event := range p.events {
		if event["event"] == name {
			n++
		}
	}
	return n
}

// until polls predicate for one step.
func (p *probe) until(predicate func() bool) bool {
	deadline := time.Now().Add(p.step)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (p *probe) wait(match func(map[string]any) bool) bool {
	return p.until(func() bool { return p.has(match) })
}

// waitWorker waits for a message from worker containing text.
func (p *probe) waitWorker(text string) bool { return p.until(func() bool { return p.said(text) }) }

func (p *probe) said(text string) bool {
	for _, message := range p.developer.Log(p.room) {
		if message.Author == "worker" && strings.Contains(message.Body, text) {
			return true
		}
	}
	return false
}

// write keeps the checks, the control events and the room for docs/dev.
func (p *probe) write(out string) {
	var b strings.Builder
	b.WriteString("# checks\n")
	for _, line := range p.checks {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n# events\n")
	p.mutex.Lock()
	for _, event := range p.events {
		raw, _ := json.Marshal(event)
		b.Write(append(raw, '\n'))
	}
	p.mutex.Unlock()
	b.WriteString("\n# room\n")
	for _, message := range p.developer.Log(p.room) {
		fmt.Fprintf(&b, "[%d] %s (%s): %q\n", message.Seq, message.Author, message.Kind, message.Body)
	}
	_ = os.WriteFile(filepath.Join(out, "transcript.txt"), []byte(b.String()), 0o644)
}

func repoRoot() string {
	_, source, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(source), "..", "..", "..")
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "hostrun: "+format+"\n", args...)
	return 2
}
