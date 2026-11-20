package broker

// The one-shot kind: a CLI run once per turn, which resumes its session by id.
//
// OneShot is both the Adapter and the pipes the relay drives, so the relay is
// the same for both kinds. A frame on its stdin starts one process; the
// process's exit ends the turn, whatever it printed, so a CLI that crashes
// cannot leave the relay waiting. An interrupt signals the running process.
// Closing stdin ends the run once the running process exits.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The run command's placeholders. An argument that is exactly resumeHolder
// becomes the spec's resume arguments once there is a session, and nothing
// before. textHolder in an argument becomes the turn's text; with none in the
// command, the text goes to the process's stdin, which keeps it out of the
// process list.
const (
	textHolder    = "{text}"
	resumeHolder  = "{resume}"
	sessionHolder = "{session}"
)

// How long an interrupted process has to exit on SIGINT before SIGKILL, and
// how long its stdout may stay open after it exits: a child it left behind
// holds the pipe. Variables so tests can shorten them.
var (
	interruptGrace = 5 * time.Second
	drainGrace     = time.Second
)

// Spec is how to read one CLI, loaded from the file `-agent-spec` names.
type Spec struct {
	// Name is the session event's `agent`.
	Name string `json:"name"`
	// Resume is the arguments that resume a session, holding {session}.
	Resume []string `json:"resume"`
	// Type is the `type` of the CLI's final record. Empty takes the last JSON
	// object it printed.
	Type string `json:"type"`
	// Dotted paths into the final record: the turn's text, the session id,
	// and the error.
	Text    string `json:"text"`
	Session string `json:"session"`
	Error   string `json:"error"`
}

// LoadSpec reads and checks a spec file.
func LoadSpec(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case spec.Name == "":
		return Spec{}, fmt.Errorf("%s: a spec needs a name", path)
	case spec.Text == "":
		return Spec{}, fmt.Errorf("%s: a spec needs a text path", path)
	case len(spec.Resume) > 0 && spec.Session == "":
		return Spec{}, fmt.Errorf("%s: resume needs a session path to fill %s", path, sessionHolder)
	}
	return spec, nil
}

// The line OneShot writes for the relay when a turn's process exits. Only the
// relay reads it; the dispatcher's passthrough gets the CLI's own bytes.
type turnLine struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Session string `json:"session,omitempty"`
}

const turnType = "minos.turn"

// The frames the relay writes to OneShot's stdin.
type oneShotFrame struct {
	Texts     []string `json:"texts,omitempty"`
	Interrupt string   `json:"interrupt,omitempty"`
}

// OneShot runs one CLI per turn. Hand Stdin and Stdout to the relay, Start
// it, and Wait for the run's end.
type OneShot struct {
	turnLines
	spec        Spec
	argv        []string
	env         []string
	stderr      io.Writer
	passthrough io.Writer

	// Stdin takes the relay's frames; Stdout gives the relay one turnLine
	// per turn, and ends when the run does.
	Stdin  io.WriteCloser
	Stdout io.Reader
	frames io.Reader
	turns  *io.PipeWriter

	// interruptGrace and drainGrace when the run began.
	grace, drain time.Duration

	mutex   sync.Mutex
	current *exec.Cmd
	session string
	code    int
	done    chan struct{}
}

// NewOneShot checks argv against the spec. env is each process's environment;
// stderr gets the CLI's stderr, and passthrough, when not nil, its stdout.
func NewOneShot(spec Spec, argv, env []string, stderr, passthrough io.Writer) (*OneShot, error) {
	resumes := 0
	for _, arg := range argv {
		if arg == resumeHolder {
			resumes++
		}
	}
	switch {
	case len(argv) == 0:
		return nil, fmt.Errorf("a one-shot agent needs a run command")
	case len(spec.Resume) > 0 && resumes != 1:
		return nil, fmt.Errorf("the run command needs %s once, where %s's resume arguments go", resumeHolder, spec.Name)
	}
	frames, stdin := io.Pipe()
	turnsOut, turnsIn := io.Pipe()
	return &OneShot{
		spec: spec, argv: argv, env: env, stderr: stderr, passthrough: passthrough,
		Stdin: stdin, Stdout: turnsOut, frames: frames, turns: turnsIn, done: make(chan struct{}),
		grace: interruptGrace, drain: drainGrace,
	}, nil
}

// Name is the spec's.
func (o *OneShot) Name() string { return o.spec.Name }

// Push is one frame for one turn. The process gets the texts as one prompt.
func (o *OneShot) Push(texts []string) ([]byte, error) { return frame(oneShotFrame{Texts: texts}) }

// Interrupt is a frame that signals the running process.
func (o *OneShot) Interrupt(id string) ([]byte, error) { return frame(oneShotFrame{Interrupt: id}) }

func frame(f oneShotFrame) ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// turnLines is the Adapter half shared by the kinds that write turnLines for
// the relay: OneShot and ACPAgent.
type turnLines struct{}

// Turn reads a turn line.
func (turnLines) Turn(line []byte) (string, bool) {
	var turn turnLine
	if json.Unmarshal(line, &turn) != nil || turn.Type != turnType {
		return "", false
	}
	return turn.Text, true
}

// Session is the id a turn line carries.
func (turnLines) Session(line []byte) (string, bool) {
	var turn turnLine
	if json.Unmarshal(line, &turn) != nil || turn.Type != turnType || turn.Session == "" {
		return "", false
	}
	return turn.Session, true
}

// Start reads frames until the relay closes stdin, running one process per
// turn frame. Stdout ends after the last process exits.
func (o *OneShot) Start() {
	go func() {
		defer close(o.done)
		var running sync.WaitGroup
		scanner := bufio.NewScanner(o.frames)
		scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for scanner.Scan() {
			var f oneShotFrame
			if json.Unmarshal(scanner.Bytes(), &f) != nil {
				continue
			}
			switch {
			case f.Interrupt != "":
				o.interrupt()
			case len(f.Texts) > 0:
				// The relay sends one turn at a time, so a turn starts only
				// after the last one's line was read.
				running.Wait()
				running.Add(1)
				go func() {
					defer running.Done()
					o.turn(strings.Join(f.Texts, "\n\n"))
				}()
			}
		}
		running.Wait()
		o.turns.Close()
	}()
}

// Wait is the run's exit status: the last process's, or 0 if none ran.
func (o *OneShot) Wait() int {
	<-o.done
	o.mutex.Lock()
	defer o.mutex.Unlock()
	return o.code
}

// Kill stops the running process, if any. Nothing else outlives the run.
func (o *OneShot) Kill() {
	o.mutex.Lock()
	command := o.current
	o.mutex.Unlock()
	if command != nil && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

// interrupt asks the running process to stop, as Ctrl-C would, and kills it
// if it has not after interruptGrace. Its group, so the tools it ran stop too.
func (o *OneShot) interrupt() {
	o.mutex.Lock()
	command := o.current
	o.mutex.Unlock()
	if command == nil || command.Process == nil {
		return
	}
	pid := command.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGINT)
	time.AfterFunc(o.grace, func() {
		o.mutex.Lock()
		still := o.current == command
		o.mutex.Unlock()
		if still {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// turn runs one process and writes its turn line. A process that cannot
// start, or prints no final record, still ends the turn, with what it said.
func (o *OneShot) turn(text string) {
	o.mutex.Lock()
	session := o.session
	o.mutex.Unlock()

	argv, piped := o.command(text, session)
	command := exec.Command(argv[0], argv[1:]...)
	command.Env = o.env
	command.Stderr = o.stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if piped {
		command.Stdin = strings.NewReader(text)
	}
	// A pipe of its own, not StdoutPipe: Wait would close that at exit,
	// mid-read, and here the read may outlast the process.
	stdout, writer, err := os.Pipe()
	if err == nil {
		command.Stdout = writer
		err = command.Start()
		writer.Close()
		if err != nil {
			stdout.Close()
		}
	}
	if err != nil {
		o.finish(1, turnLine{Text: fmt.Sprintf("[turn failed: cannot start %s: %v]", argv[0], err)})
		return
	}
	defer stdout.Close()
	o.mutex.Lock()
	o.current = command
	o.mutex.Unlock()

	var stream io.Reader = stdout
	if o.passthrough != nil {
		stream = io.TeeReader(stdout, o.passthrough)
	}
	read := make(chan map[string]any, 1)
	go func() { read <- o.final(stream) }()
	code := exitCode(command.Wait())
	var final map[string]any
	select {
	case final = <-read:
	case <-time.After(o.drain):
		// A child still holds stdout. The turn is over; so is the child.
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		stdout.Close()
		final = <-read
	}

	o.mutex.Lock()
	o.current = nil
	o.mutex.Unlock()
	o.finish(code, o.read(final, code))
}

// command is the run command for this turn, and whether the text goes to
// stdin because no argument holds it.
func (o *OneShot) command(text, session string) ([]string, bool) {
	argv := make([]string, 0, len(o.argv)+len(o.spec.Resume))
	piped := true
	for _, arg := range o.argv {
		if arg == resumeHolder {
			if session != "" {
				for _, resume := range o.spec.Resume {
					argv = append(argv, strings.ReplaceAll(resume, sessionHolder, session))
				}
			}
			continue
		}
		if strings.Contains(arg, textHolder) {
			piped = false
			arg = strings.ReplaceAll(arg, textHolder, text)
		}
		argv = append(argv, arg)
	}
	return argv, piped
}

// final is the CLI's final record: the last JSON object of the spec's type,
// or the last JSON object at all when the spec names no type.
func (o *OneShot) final(stdout io.Reader) map[string]any {
	var found map[string]any
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if o.spec.Type == "" || record["type"] == o.spec.Type {
			found = record
		}
	}
	// What a line too long for the scanner leaves unread would block the
	// process on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	return found
}

// read is the turn line for a process's final record and exit status. The
// session carries over when a turn reports none, so one failed turn does not
// lose the conversation.
func (o *OneShot) read(final map[string]any, code int) turnLine {
	line := turnLine{Text: field(final, o.spec.Text)}
	if id := field(final, o.spec.Session); id != "" {
		line.Session = id
	}
	if line.Text == "" {
		switch failure := field(final, o.spec.Error); {
		case failure != "":
			line.Text = "[turn failed: " + failure + "]"
		case final == nil:
			line.Text = fmt.Sprintf("[turn failed: %s printed no result and exited %d]", o.spec.Name, code)
		}
	}
	return line
}

// finish records the turn's outcome and hands the relay its line.
func (o *OneShot) finish(code int, line turnLine) {
	o.mutex.Lock()
	o.code = code
	if line.Session != "" {
		o.session = line.Session
	} else {
		line.Session = o.session
	}
	o.mutex.Unlock()
	line.Type = turnType
	raw, err := json.Marshal(line)
	if err == nil {
		_, _ = o.turns.Write(append(raw, '\n'))
	}
}

// field is the string at a dotted path in a record, or "" when there is none.
func field(record map[string]any, path string) string {
	if record == nil || path == "" {
		return ""
	}
	var value any = record
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

// exitCode is a process's exit status. A process ended by a signal has none,
// and counts as 1.
func exitCode(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		return exit.ExitCode()
	default:
		return 1
	}
}
