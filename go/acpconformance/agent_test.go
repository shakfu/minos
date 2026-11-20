// Package acpconformance checks an agent's end of the Agent Client Protocol,
// version 1, as minosb uses it. It runs the command in ACP_AGENT through
// /bin/sh, or the scripted agent in acpfake when ACP_AGENT is unset.
//
//	make acp-conformance ACP_AGENT='myra --acp'
//
// A rule the spec states as MUST fails the test. One stated as SHOULD is
// logged, and fails only with ACP_STRICT=1. Each test cites its page under
// https://agentclientprotocol.com/protocol/.
//
// Prompts reach a model, so the agent is run against whatever mock its own
// tests use. The prompts come from the environment:
//
//	ACP_PROMPT             any prompt the agent answers (default "say hello")
//	ACP_SLOW_PROMPT        one still running 1 s after it starts; unset skips the cancel test
//	ACP_PERMISSION_PROMPT  one that makes the agent ask permission; unset skips that test
package acpconformance

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"minos/internal/acp"
	"minos/internal/acp/acpfake"
)

const fakeAgentEnv = "MINOS_ACP_FAKE_DIR"

// How long the agent may take to answer a setup request, and a prompt.
const (
	setupLimit  = 10 * time.Second
	promptLimit = 60 * time.Second
)

// The fake's history lives here for the whole run, so session/load in a
// second process finds what the first saved.
var fakeDir string

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeAgentEnv); dir != "" {
		(&acpfake.Agent{Dir: dir}).Serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	if os.Getenv("ACP_AGENT") == "" {
		dir, err := os.MkdirTemp("", "acpfake")
		if err != nil {
			panic(err)
		}
		fakeDir = dir
		defer os.RemoveAll(dir)
		os.Setenv("ACP_SLOW_PROMPT", "slow")
		os.Setenv("ACP_PERMISSION_PROMPT", "permission")
	}
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

// should reports a SHOULD rule broken: logged, or failed under ACP_STRICT.
func should(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("ACP_STRICT") == "1" {
		t.Errorf("SHOULD: "+format, args...)
	} else {
		t.Logf("SHOULD (not failed without ACP_STRICT=1): "+format, args...)
	}
}

func prompt() string {
	if p := os.Getenv("ACP_PROMPT"); p != "" {
		return p
	}
	return "say hello"
}

// agent is one running agent and what the test saw of it.
type agent struct {
	conn    *acp.Conn
	command *exec.Cmd
	stdin   io.WriteCloser
	cwd     string

	mutex sync.Mutex
	// Lines that were not JSON-RPC 2.0 messages.
	bad []string
	// Updates, in order, with their session.
	updates []seen
	// Agent-to-client requests by method.
	requests []string
	// permission answers each permission request; nil cancels.
	permission func(params json.RawMessage) any
}

type seen struct {
	session string
	update  acp.Update
}

// start runs the agent in cwd, a new directory unless given.
func start(t *testing.T, cwd ...string) *agent {
	t.Helper()
	dir := t.TempDir()
	if len(cwd) > 0 {
		dir = cwd[0]
	}
	var command *exec.Cmd
	if line := os.Getenv("ACP_AGENT"); line != "" {
		command = exec.Command("/bin/sh", "-c", line)
	} else {
		command = exec.Command(os.Args[0])
		command.Env = append(os.Environ(), fakeAgentEnv+"="+fakeDir)
	}
	command.Dir = dir
	command.Stderr = os.Stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("cannot start the agent: %v", err)
	}
	a := &agent{command: command, stdin: stdin, cwd: dir}
	a.conn = acp.NewConn(stdin, a)
	a.conn.Line = func(line []byte, err error) {
		if err != nil {
			a.mutex.Lock()
			a.bad = append(a.bad, string(line))
			a.mutex.Unlock()
		}
	}
	go a.conn.Serve(stdout)
	t.Cleanup(func() {
		stdin.Close()
		select {
		case <-a.conn.Done():
		case <-time.After(5 * time.Second):
			t.Error("SHOULD: the agent did not exit within 5 s of its stdin closing")
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		a.mutex.Lock()
		defer a.mutex.Unlock()
		// transports: the agent MUST NOT write anything to stdout that is
		// not a valid ACP message.
		for _, line := range a.bad {
			t.Errorf("MUST: stdout carried a line that is not a JSON-RPC 2.0 message: %.200q", line)
		}
		// file-system, terminals: MUST NOT call what the client did not
		// advertise, and this client advertises neither.
		for _, method := range a.requests {
			if strings.HasPrefix(method, "fs/") || strings.HasPrefix(method, "terminal/") {
				t.Errorf("MUST: the agent called %s, which the client did not advertise", method)
			}
		}
	})
	return a
}

func (a *agent) Request(method string, params json.RawMessage, reply func(any, *acp.Error)) {
	a.mutex.Lock()
	a.requests = append(a.requests, method)
	answer := a.permission
	a.mutex.Unlock()
	if method != "session/request_permission" {
		reply(nil, &acp.Error{Code: acp.CodeMethodNotFound, Message: "Method not found: " + method})
		return
	}
	if answer != nil {
		reply(answer(params), nil)
		return
	}
	reply(map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil)
}

func (a *agent) Notification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var p acp.UpdateParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	update, err := acp.ParseUpdate(p.Update)
	if err != nil {
		return
	}
	a.mutex.Lock()
	a.updates = append(a.updates, seen{session: p.SessionID, update: update})
	a.mutex.Unlock()
}

func (a *agent) seen() []seen {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	return slices.Clone(a.updates)
}

func (a *agent) initialize(t *testing.T) acp.InitializeResult {
	t.Helper()
	var result acp.InitializeResult
	err := a.conn.CallWithin(setupLimit, "initialize", map[string]any{
		"protocolVersion": acp.ProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false,
		},
		"clientInfo": acp.Info{Name: "acpconformance", Version: "1"},
	}, &result)
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return result
}

func (a *agent) newSession(t *testing.T) string {
	t.Helper()
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := a.conn.CallWithin(setupLimit, "session/new", map[string]any{"cwd": a.cwd, "mcpServers": []any{}}, &result); err != nil {
		t.Fatalf("session/new failed: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("MUST: session/new answered no sessionId")
	}
	return result.SessionID
}

func (a *agent) prompt(session, text string) (string, error) {
	var result struct {
		StopReason string `json:"stopReason"`
	}
	err := a.conn.CallWithin(promptLimit, "session/prompt", map[string]any{
		"sessionId": session, "prompt": []acp.ContentBlock{{Type: "text", Text: text}},
	}, &result)
	return result.StopReason, err
}

// code is a JSON-RPC error's code, or 0.
func code(err error) int {
	var failure *acp.Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return 0
}

// initialization: the agent MUST answer with the client's version when it
// supports it, and with agentCapabilities.
func TestInitializeAnswersVersion1AndCapabilities(t *testing.T) {
	result := start(t).initialize(t)
	if result.ProtocolVersion != acp.ProtocolVersion {
		t.Errorf("MUST: initialize answered protocolVersion %d to a client asking for %d", result.ProtocolVersion, acp.ProtocolVersion)
	}
	if result.AgentCapabilities == nil {
		t.Error("MUST: initialize answered no agentCapabilities")
	}
}

// initialization: before initialize, the agent SHOULD refuse other requests
// with -32600.
func TestARequestBeforeInitializeIsRefused(t *testing.T) {
	a := start(t)
	err := a.conn.CallWithin(setupLimit, "session/new", map[string]any{"cwd": a.cwd, "mcpServers": []any{}}, nil)
	should(t, code(err) == acp.CodeInvalidRequest, "session/new before initialize answered %v, not -32600", err)
}

// JSON-RPC 2.0: an unknown method is -32601.
func TestAnUnknownMethodIsMethodNotFound(t *testing.T) {
	a := start(t)
	a.initialize(t)
	if err := a.conn.CallWithin(setupLimit, "minos/no_such_method", map[string]any{}, nil); code(err) != acp.CodeMethodNotFound {
		t.Errorf("MUST: an unknown method answered %v, not -32601", err)
	}
}

// session-setup, prompt-turn: a session takes a text prompt and the turn
// ends with one of the five stop reasons. Its updates name the session and
// come before the response.
func TestAPromptEndsWithAStopReason(t *testing.T) {
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	stop, err := a.prompt(session, prompt())
	if err != nil {
		t.Fatalf("session/prompt failed: %v", err)
	}
	if !slices.Contains(acp.StopReasons, stop) {
		t.Errorf("MUST: the prompt ended with stopReason %q, not one of %v", stop, acp.StopReasons)
	}
	updates := a.seen()
	for _, s := range updates {
		if s.session != session {
			t.Errorf("an update named session %q, not %q", s.session, session)
		}
	}
	should(t, slices.ContainsFunc(updates, func(s seen) bool { return s.update.SessionUpdate == "agent_message_chunk" }),
		"the turn sent no agent_message_chunk before its response")
}

// cancellation: on session/cancel the agent MUST answer the pending prompt
// with stopReason cancelled, not an error.
func TestCancelEndsThePromptCancelled(t *testing.T) {
	slow := os.Getenv("ACP_SLOW_PROMPT")
	if slow == "" {
		t.Skip("ACP_SLOW_PROMPT is unset")
	}
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	type answer struct {
		stop string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		stop, err := a.prompt(session, slow)
		done <- answer{stop, err}
	}()
	time.Sleep(time.Second)
	if err := a.conn.Notify("session/cancel", map[string]string{"sessionId": session}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		switch {
		case got.err != nil:
			t.Errorf("MUST: a cancelled prompt answered an error: %v", got.err)
		case got.stop != "cancelled":
			t.Errorf("MUST: a cancelled prompt ended with stopReason %q", got.stop)
		}
	case <-time.After(10 * time.Second):
		t.Error("SHOULD: the prompt was still running 10 s after session/cancel")
	}
}

// prompt-turn: a cancelled permission request is not approval. The turn goes
// on and ends; the agent must not have taken the tool call as allowed, which
// only its own transcript can show, so this checks the turn ends cleanly.
func TestACancelledPermissionRequestEndsTheTurnCleanly(t *testing.T) {
	ask := os.Getenv("ACP_PERMISSION_PROMPT")
	if ask == "" {
		t.Skip("ACP_PERMISSION_PROMPT is unset")
	}
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	stop, err := a.prompt(session, ask)
	if err != nil {
		t.Fatalf("the prompt failed: %v", err)
	}
	a.mutex.Lock()
	asked := slices.Contains(a.requests, "session/request_permission")
	a.mutex.Unlock()
	if !asked {
		t.Fatalf("ACP_PERMISSION_PROMPT %q did not make the agent ask permission", ask)
	}
	if !slices.Contains(acp.StopReasons, stop) {
		t.Errorf("MUST: the turn ended with stopReason %q", stop)
	}
	for _, s := range a.seen() {
		if s.update.SessionUpdate == "tool_call_update" && s.update.Status == "completed" && s.update.ToolCallID != "" {
			t.Logf("a tool call completed after its permission was cancelled: %s; check it was not the one asked about", s.update.ToolCallID)
		}
	}
}

// session-setup: with loadSession, session/load in a new process MUST replay
// the history as session/update before its response.
func TestSessionLoadReplaysTheHistory(t *testing.T) {
	first := start(t)
	if !first.initialize(t).AgentCapabilities.LoadSession {
		t.Skip("the agent does not advertise loadSession")
	}
	session := first.newSession(t)
	if _, err := first.prompt(session, prompt()); err != nil {
		t.Fatalf("session/prompt failed: %v", err)
	}
	first.stdin.Close()
	<-first.conn.Done()

	second := start(t, first.cwd)
	second.initialize(t)
	if err := second.conn.CallWithin(promptLimit, "session/load", map[string]any{"sessionId": session, "cwd": first.cwd, "mcpServers": []any{}}, nil); err != nil {
		t.Fatalf("session/load failed: %v", err)
	}
	updates := second.seen()
	if !slices.ContainsFunc(updates, func(s seen) bool { return s.update.SessionUpdate == "user_message_chunk" }) {
		t.Errorf("MUST: session/load replayed no user_message_chunk before its response; it sent %d updates", len(updates))
	}
}
