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
//	ACP_EXPECT_USAGE=1     fail, rather than skip, when the agent sends no usage_update
//
// Each test gives the agent a new HOME and XDG_*_HOME, so its sessions and
// config stay out of the person's. An assignment in ACP_AGENT overrides them.
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
	gate    *gate
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

// Each test's home, so an agent's sessions and config stay out of the
// person's own, and a test's second agent finds its first agent's sessions.
var homes sync.Map

// home is t's own HOME and XDG directories, as environment entries.
func home(t *testing.T) []string {
	dir, ok := homes.Load(t)
	if !ok {
		dir = t.TempDir()
		homes.Store(t, dir)
		t.Cleanup(func() { homes.Delete(t) })
	}
	root := dir.(string)
	env := []string{"HOME=" + root}
	for _, name := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		env = append(env, "XDG_"+name+"_HOME="+root+"/."+strings.ToLower(name))
	}
	return env
}

// start runs the agent in cwd, a new directory unless given, with the
// test's own home.
func start(t *testing.T, cwd ...string) *agent {
	t.Helper()
	dir := t.TempDir()
	if len(cwd) > 0 {
		dir = cwd[0]
	}
	var command *exec.Cmd
	if line := os.Getenv("ACP_AGENT"); line != "" {
		command = exec.Command("/bin/sh", "-c", line)
		command.Env = append(os.Environ(), home(t)...)
	} else {
		command = exec.Command(os.Args[0])
		command.Env = append(os.Environ(), append(home(t), fakeAgentEnv+"="+fakeDir)...)
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
	a := &agent{command: command, stdin: stdin, cwd: dir, gate: &gate{w: stdin}}
	a.conn = acp.NewConn(a.gate, a)
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

// gate passes writes through, or holds them so several messages reach the
// agent in one write.
type gate struct {
	mutex   sync.Mutex
	w       io.Writer
	holding bool
	held    []byte
}

func (g *gate) Write(p []byte) (int, error) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	if g.holding {
		g.held = append(g.held, p...)
		return len(p), nil
	}
	return g.w.Write(p)
}

func (g *gate) hold() {
	g.mutex.Lock()
	g.holding = true
	g.mutex.Unlock()
}

// lines is how many messages are held.
func (g *gate) lines() int {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return strings.Count(string(g.held), "\n")
}

// release writes what is held in one write, and passes later writes through.
func (g *gate) release() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.holding = false
	_, err := g.w.Write(g.held)
	g.held = nil
	return err
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

// prompt-turn, cancellation: a cancel that reaches the agent with its prompt,
// in one write, still cancels that prompt. An agent that starts the turn
// before it can take a cancel loses this one.
func TestACancelInThePromptsOwnWriteEndsItCancelled(t *testing.T) {
	slow := os.Getenv("ACP_SLOW_PROMPT")
	if slow == "" {
		t.Skip("ACP_SLOW_PROMPT is unset")
	}
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	a.gate.hold()
	type answer struct {
		stop string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		stop, err := a.prompt(session, slow)
		done <- answer{stop, err}
	}()
	for a.gate.lines() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := a.conn.Notify("session/cancel", map[string]string{"sessionId": session}); err != nil {
		t.Fatal(err)
	}
	if err := a.gate.release(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		switch {
		case got.err != nil:
			t.Errorf("MUST: a cancelled prompt answered an error: %v", got.err)
		case got.stop != "cancelled":
			t.Errorf("MUST: a prompt cancelled in its own write ended with stopReason %q", got.stop)
		}
	case <-time.After(10 * time.Second):
		t.Error("MUST: a prompt cancelled in its own write was still running 10 s later")
	}
}

// prompt-turn, cancellation: every prompt gets a response, however soon its
// cancel follows. Twenty prompts of ACP_PROMPT, each with its cancel in the
// same write, so a cancel that lands before the turn starts is hit. Each may
// end cancelled or finish first; the agent MUST answer each, and the session
// MUST then take the next. A JSON-RPC error counts as an answer: a mock may
// run out of turns.
func TestEveryQuicklyCancelledPromptIsAnswered(t *testing.T) {
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	for round := range 20 {
		a.gate.hold()
		done := make(chan error, 1)
		var stop string
		go func() {
			var err error
			stop, err = a.prompt(session, prompt())
			done <- err
		}()
		for a.gate.lines() == 0 {
			time.Sleep(time.Millisecond)
		}
		if err := a.conn.Notify("session/cancel", map[string]string{"sessionId": session}); err != nil {
			t.Fatal(err)
		}
		if err := a.gate.release(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			switch {
			case err != nil && code(err) == acp.CodeInvalidRequest:
				t.Fatalf("MUST: round %d: the session refused its next prompt after a cancel: %v", round, err)
			case err != nil:
				t.Logf("round %d: %v", round, err)
			case !slices.Contains(acp.StopReasons, stop):
				t.Errorf("MUST: round %d ended with stopReason %q", round, stop)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("MUST: round %d: a prompt cancelled in its own write got no answer in 10 s", round)
		}
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

// prompt-turn, cancellation: once the client cancels, it MUST answer each
// pending permission request cancelled, and the agent MUST end the prompt
// with stopReason cancelled. The agent SHOULD then stop: no tool it asked
// about runs, and it asks nothing more.
func TestCancelDuringAPermissionRequestEndsThePromptCancelled(t *testing.T) {
	ask := os.Getenv("ACP_PERMISSION_PROMPT")
	if ask == "" {
		t.Skip("ACP_PERMISSION_PROMPT is unset")
	}
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	var asked []string
	var after int
	a.permission = func(params json.RawMessage) any {
		var request acp.PermissionRequest
		_ = json.Unmarshal(params, &request)
		a.mutex.Lock()
		if len(asked) > 0 {
			after++
		}
		asked = append(asked, request.ToolCall.ToolCallID)
		a.mutex.Unlock()
		if err := a.conn.Notify("session/cancel", map[string]string{"sessionId": session}); err != nil {
			t.Error(err)
		}
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	}
	stop, err := a.prompt(session, ask)
	switch {
	case err != nil:
		t.Fatalf("MUST: a cancelled prompt answered an error: %v", err)
	case stop != "cancelled":
		t.Errorf("MUST: a prompt cancelled during a permission request ended with stopReason %q", stop)
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if len(asked) == 0 {
		t.Fatalf("ACP_PERMISSION_PROMPT %q did not make the agent ask permission", ask)
	}
	should(t, after == 0, "the agent asked permission %d more time(s) after the cancel", after)
	for _, s := range a.updates {
		if s.update.SessionUpdate == "tool_call_update" && s.update.Status == "completed" && s.update.ToolCallID == asked[0] {
			should(t, false, "tool call %s completed after the cancel", asked[0])
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

// prompt-turn, "Session usage updates": usage_update carries `used` and `size`,
// both required. A cost, if present, MUST carry `amount` and `currency`. The
// cost is cumulative for the session, so it never falls from one update to the
// next. Two turns, so the cost crosses a turn boundary.
func TestUsageUpdatesAreWellFormedAndCumulative(t *testing.T) {
	a := start(t)
	a.initialize(t)
	session := a.newSession(t)
	for range 2 {
		if _, err := a.prompt(session, prompt()); err != nil {
			t.Fatalf("session/prompt failed: %v", err)
		}
	}
	var usages []seen
	for _, s := range a.seen() {
		if s.update.SessionUpdate == "usage_update" {
			usages = append(usages, s)
		}
	}
	if len(usages) == 0 {
		if os.Getenv("ACP_EXPECT_USAGE") == "1" {
			t.Fatal("the agent sent no usage_update in two turns")
		}
		t.Skip("the agent sent no usage_update; ACP_EXPECT_USAGE=1 makes that a failure")
	}
	last := -1.0
	for _, s := range usages {
		var u struct {
			Used *float64 `json:"used"`
			Size *float64 `json:"size"`
			Cost *struct {
				Amount   *float64 `json:"amount"`
				Currency *string  `json:"currency"`
			} `json:"cost"`
		}
		if err := json.Unmarshal(s.update.Raw, &u); err != nil {
			t.Errorf("MUST: a usage_update is not well-formed: %v: %s", err, s.update.Raw)
			continue
		}
		if u.Used == nil || u.Size == nil || *u.Used < 0 || *u.Size < 0 {
			t.Errorf("MUST: a usage_update lacks used or size, or one is negative: %s", s.update.Raw)
		}
		if u.Cost == nil {
			continue
		}
		if u.Cost.Amount == nil || u.Cost.Currency == nil || *u.Cost.Currency == "" {
			t.Errorf("MUST: a usage_update cost lacks amount or currency: %s", s.update.Raw)
			continue
		}
		if *u.Cost.Amount < last {
			t.Errorf("MUST: the session's cost fell from %v to %v, though it is cumulative", last, *u.Cost.Amount)
		}
		last = *u.Cost.Amount
	}
	t.Logf("%d usage_update(s); last cost %v", len(usages), last)
}

// lastCost is the cost of the last usage_update that has one, or -1.
func lastCost(updates []seen) float64 {
	cost := -1.0
	for _, s := range updates {
		if s.update.SessionUpdate != "usage_update" {
			continue
		}
		var u struct {
			Cost *struct {
				Amount float64 `json:"amount"`
			} `json:"cost"`
		}
		if json.Unmarshal(s.update.Raw, &u) == nil && u.Cost != nil {
			cost = u.Cost.Amount
		}
	}
	return cost
}

// prompt-turn, "Session usage updates": the cost is the session's, so a
// session loaded in a new process goes on from what it had cost. Three
// prompts of ACP_PROMPT: two before the load, one after.
func TestTheCostGoesOnAfterASessionLoad(t *testing.T) {
	first := start(t)
	if !first.initialize(t).AgentCapabilities.LoadSession {
		t.Skip("the agent does not advertise loadSession")
	}
	session := first.newSession(t)
	// Two turns, so a cost that restarts at the load falls below this one.
	for range 2 {
		if _, err := first.prompt(session, prompt()); err != nil {
			t.Fatalf("session/prompt failed: %v", err)
		}
	}
	first.stdin.Close()
	<-first.conn.Done()
	before := lastCost(first.seen())
	if before < 0 {
		if os.Getenv("ACP_EXPECT_USAGE") == "1" {
			t.Fatal("the agent sent no usage_update with a cost")
		}
		t.Skip("the agent sent no usage_update with a cost; ACP_EXPECT_USAGE=1 makes that a failure")
	}

	second := start(t, first.cwd)
	second.initialize(t)
	if err := second.conn.CallWithin(promptLimit, "session/load", map[string]any{"sessionId": session, "cwd": first.cwd, "mcpServers": []any{}}, nil); err != nil {
		t.Fatalf("session/load failed: %v", err)
	}
	if _, err := second.prompt(session, prompt()); err != nil {
		t.Fatalf("session/prompt after the load failed: %v", err)
	}
	after := lastCost(second.seen())
	if after < before {
		t.Errorf("MUST: the session's cost fell from %v to %v across a session/load, though it is cumulative", before, after)
	}
	t.Logf("cost %v before the load, %v after", before, after)
}
