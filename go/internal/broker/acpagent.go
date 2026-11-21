package broker

// The ACP kind: one long-lived agent that speaks the Agent Client Protocol on
// its stdio (internal/acp). Like OneShot it is both the Adapter and the pipes
// the relay drives. A frame is one session/prompt; the turn's text is its
// agent_message_chunk updates; an interrupt is session/cancel. The client
// advertises no file system and no terminal, so the agent uses its own.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"minos/internal/acp"
)

// How long the agent has to exit once its stdin closes, before SIGKILL. A
// variable so tests can shorten it.
var acpExitGrace = 5 * time.Second

// ACPAgent runs one ACP agent for the run. Hand Stdin and Stdout to the
// relay, Start it, and Wait for the run's end.
type ACPAgent struct {
	turnLines
	name    string
	session string
	command *exec.Cmd
	stdin   io.WriteCloser
	client  *acp.Client
	exited  chan struct{}
	report  func(map[string]any)
	allow   bool

	Stdin  io.WriteCloser
	Stdout io.Reader
	frames io.Reader
	turns  *io.PipeWriter

	mutex sync.Mutex
	text  strings.Builder
	// A tool call came after the turn's text: the next text is a new message.
	split bool
	code  int
	done  chan struct{}
}

// NewACP starts argv, initializes it, and opens a session in cwd, an absolute
// path. stderr gets the agent's stderr, passthrough when not nil its stdout,
// and report the `result`, `usage` and `permission` control events. allow
// grants each permission request; otherwise each is denied.
func NewACP(argv, env []string, cwd string, stderr, passthrough io.Writer, report func(map[string]any), allow bool) (*ACPAgent, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("an ACP agent needs a run command")
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Env = env
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	if report == nil {
		report = func(map[string]any) {}
	}

	frames, framesIn := io.Pipe()
	turnsOut, turnsIn := io.Pipe()
	a := &ACPAgent{
		command: command, stdin: stdin, client: acp.NewClient(stdin), exited: make(chan struct{}), report: report, allow: allow,
		Stdin: framesIn, Stdout: turnsOut, frames: frames, turns: turnsIn, done: make(chan struct{}),
	}
	a.client.OnUpdate = a.update
	a.client.OnPermission = a.permission

	var stream io.Reader = stdout
	if passthrough != nil {
		stream = io.TeeReader(stdout, passthrough)
	}
	go func() {
		a.client.Serve(stream)
		// What the agent still writes after a bad line keeps it from
		// blocking on a full pipe.
		_, _ = io.Copy(io.Discard, stream)
		a.code = exitCode(command.Wait())
		close(a.exited)
	}()

	result, err := a.client.Initialize(acp.Info{Name: "minosb", Version: "1"})
	if err == nil {
		a.name = "acp"
		if result.AgentInfo != nil && result.AgentInfo.Name != "" {
			a.name = result.AgentInfo.Name
		}
		a.session, err = a.client.NewSession(cwd)
	}
	if err != nil {
		a.Kill()
		<-a.exited
		return nil, fmt.Errorf("%s did not start an ACP session: %w", argv[0], err)
	}
	return a, nil
}

// Name is the agent's own, from its initialize result.
func (a *ACPAgent) Name() string { return a.name }

// Push is one frame for one prompt.
func (a *ACPAgent) Push(texts []string) ([]byte, error) { return frame(oneShotFrame{Texts: texts}) }

// Interrupt is a frame that cancels the running prompt.
func (a *ACPAgent) Interrupt(id string) ([]byte, error) { return frame(oneShotFrame{Interrupt: id}) }

// Start reads frames until the relay closes stdin, prompting once per turn
// frame. Then it closes the agent's stdin, waits for it to exit, and ends
// Stdout.
func (a *ACPAgent) Start() {
	go func() {
		defer close(a.done)
		var running sync.WaitGroup
		scanner := bufio.NewScanner(a.frames)
		scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for scanner.Scan() {
			var f oneShotFrame
			if json.Unmarshal(scanner.Bytes(), &f) != nil {
				continue
			}
			switch {
			case f.Interrupt != "":
				_ = a.client.Cancel(a.session)
			case len(f.Texts) > 0:
				running.Wait()
				running.Add(1)
				go func() {
					defer running.Done()
					a.prompt(strings.Join(f.Texts, "\n\n"))
				}()
			}
		}
		running.Wait()
		_ = a.stdin.Close()
		select {
		case <-a.exited:
		case <-time.After(acpExitGrace):
			a.Kill()
			<-a.exited
		}
		a.turns.Close()
	}()
}

// Wait is the agent's exit status.
func (a *ACPAgent) Wait() int {
	<-a.done
	return a.code
}

// Kill stops the agent and the tools it ran.
func (a *ACPAgent) Kill() {
	if a.command.Process != nil {
		_ = syscall.Kill(-a.command.Process.Pid, syscall.SIGKILL)
	}
}

// prompt runs one turn and writes its turn line. A prompt the agent fails is
// said in the room, as a one-shot crash is.
func (a *ACPAgent) prompt(text string) {
	a.mutex.Lock()
	a.text.Reset()
	a.split = false
	a.mutex.Unlock()
	stop, err := a.client.Prompt(a.session, text)
	a.mutex.Lock()
	said := a.text.String()
	a.mutex.Unlock()
	// The whole reply, for a dispatcher that reads the run from its events;
	// the room's copy may be cut.
	result := map[string]any{"event": "result", "text": said, "stop_reason": stop}
	line := turnLine{Type: turnType, Text: said, Session: a.session}
	switch {
	case err != nil:
		result["error"] = err.Error()
		line.Text = strings.TrimSpace(said + "\n\n[turn failed: " + err.Error() + "]")
	case stop != "end_turn" && stop != "cancelled":
		line.Text = strings.TrimSpace(said + "\n\n[stopped: " + stop + "]")
	}
	if err != nil {
		delete(result, "stop_reason")
	}
	a.report(result)
	raw, _ := json.Marshal(line)
	_, _ = a.turns.Write(append(raw, '\n'))
}

// update gathers the running turn's reply, and reports the cost the agent
// gives in a usage_update.
func (a *ACPAgent) update(session string, update acp.Update) {
	if session != a.session {
		return
	}
	switch update.SessionUpdate {
	case "agent_message_chunk":
		if update.Content != nil && update.Content.Type == "text" {
			a.mutex.Lock()
			a.say(update.Content.Text)
			a.mutex.Unlock()
		}
	case "tool_call", "tool_call_update":
		a.mutex.Lock()
		a.split = a.text.Len() > 0
		a.mutex.Unlock()
	case "usage_update":
		var usage struct {
			Cost *struct {
				Amount   float64 `json:"amount"`
				Currency string  `json:"currency"`
			} `json:"cost"`
		}
		if json.Unmarshal(update.Raw, &usage) == nil && usage.Cost != nil {
			a.report(map[string]any{"event": "usage", "cost": usage.Cost.Amount, "currency": usage.Cost.Currency})
		}
	}
}

// say adds a chunk to the turn's text, as a new paragraph after a tool call.
// ACP marks no message boundary, so the two messages would run together.
// Call it with the mutex held.
func (a *ACPAgent) say(chunk string) {
	if !a.split {
		a.text.WriteString(chunk)
		return
	}
	if chunk = strings.TrimLeft(chunk, "\n"); chunk == "" {
		return
	}
	before := strings.TrimRight(a.text.String(), " \n")
	a.text.Reset()
	a.text.WriteString(before + "\n\n" + chunk)
	a.split = false
}

// How much of a tool call's input a permission event carries.
const permissionInputLimit = 200

// permission answers each request by the run's policy and reports it, until
// a person can answer from the room. Once, never always, so the policy is
// asked again next time. A request with no option of the policy's kind is
// cancelled, which the agent must take as a denial.
func (a *ACPAgent) permission(request acp.PermissionRequest) (string, bool) {
	input := string(request.ToolCall.RawInput)
	if len(input) > permissionInputLimit {
		input = strings.ToValidUTF8(input[:permissionInputLimit], "") + "..."
	}
	event := map[string]any{
		"event": "permission", "tool": request.ToolCall.Title, "kind": request.ToolCall.Kind,
		"input": input, "outcome": "denied", "by": "policy",
	}
	defer func() { a.report(event) }()
	want := "reject_once"
	if a.allow {
		want = "allow_once"
	}
	for _, option := range request.Options {
		if option.Kind == want {
			if a.allow {
				event["outcome"] = "allowed"
			}
			return option.OptionID, true
		}
	}
	return "", false
}
