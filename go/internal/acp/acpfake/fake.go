// Package acpfake is a scripted ACP agent, for tests of the client and of the
// conformance suite itself. It needs no model.
//
// A prompt is echoed as "you said: TEXT" in two chunks. A prompt holding
// "slow" waits for session/cancel. One holding "permission" asks the client
// first and reports its answer. History is kept in Dir, so session/load in a
// new process replays it.
package acpfake

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"minos/internal/acp"
)

// Agent serves one connection.
type Agent struct {
	// Dir holds each session's history, one file per id.
	Dir string

	conn        *acp.Conn
	mutex       sync.Mutex
	initialized bool
	sessions    int
	cancels     map[string]chan struct{}
}

// Serve answers r on w until r ends.
func (a *Agent) Serve(r io.Reader, w io.Writer) {
	a.cancels = map[string]chan struct{}{}
	a.conn = acp.NewConn(w, a)
	a.conn.Serve(r)
}

type turn struct {
	User  string `json:"user"`
	Agent string `json:"agent"`
}

func (a *Agent) Request(method string, params json.RawMessage, reply func(any, *acp.Error)) {
	a.mutex.Lock()
	initialized := a.initialized
	a.mutex.Unlock()
	if method != "initialize" && !initialized {
		reply(nil, &acp.Error{Code: acp.CodeInvalidRequest, Message: "initialize first"})
		return
	}
	switch method {
	case "initialize":
		a.mutex.Lock()
		a.initialized = true
		a.mutex.Unlock()
		reply(map[string]any{
			"protocolVersion":   acp.ProtocolVersion,
			"agentCapabilities": map[string]any{"loadSession": true},
			"agentInfo":         acp.Info{Name: "fake", Version: "0"},
			"authMethods":       []any{},
		}, nil)
	case "session/new":
		var p struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(params, &p) != nil || !filepath.IsAbs(p.Cwd) {
			reply(nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "cwd must be an absolute path"})
			return
		}
		a.mutex.Lock()
		a.sessions++
		id := fmt.Sprintf("fake-%d-%d", os.Getpid(), a.sessions)
		a.mutex.Unlock()
		if err := a.save(id, nil); err != nil {
			reply(nil, &acp.Error{Code: acp.CodeInternal, Message: err.Error()})
			return
		}
		reply(map[string]string{"sessionId": id}, nil)
	case "session/load":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &p)
		history, err := a.load(p.SessionID)
		if err != nil {
			reply(nil, &acp.Error{Code: acp.CodeNotFound, Message: "no session " + p.SessionID})
			return
		}
		for _, t := range history {
			a.update(p.SessionID, "user_message_chunk", t.User)
			a.update(p.SessionID, "agent_message_chunk", t.Agent)
		}
		reply(struct{}{}, nil)
	case "session/prompt":
		var p struct {
			SessionID string             `json:"sessionId"`
			Prompt    []acp.ContentBlock `json:"prompt"`
		}
		if json.Unmarshal(params, &p) != nil {
			reply(nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "unreadable prompt"})
			return
		}
		history, err := a.load(p.SessionID)
		if err != nil {
			reply(nil, &acp.Error{Code: acp.CodeNotFound, Message: "no session " + p.SessionID})
			return
		}
		go a.prompt(p.SessionID, p.Prompt, history, reply)
	default:
		reply(nil, &acp.Error{Code: acp.CodeMethodNotFound, Message: "Method not found: " + method})
	}
}

func (a *Agent) Notification(method string, params json.RawMessage) {
	if method != "session/cancel" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	a.mutex.Lock()
	if cancel, ok := a.cancels[p.SessionID]; ok {
		close(cancel)
		delete(a.cancels, p.SessionID)
	}
	a.mutex.Unlock()
}

func (a *Agent) prompt(session string, blocks []acp.ContentBlock, history []turn, reply func(any, *acp.Error)) {
	var texts []string
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	text := strings.Join(texts, "\n")
	cancel := make(chan struct{})
	a.mutex.Lock()
	a.cancels[session] = cancel
	a.mutex.Unlock()
	defer func() {
		a.mutex.Lock()
		if a.cancels[session] == cancel {
			delete(a.cancels, session)
		}
		a.mutex.Unlock()
	}()

	answer := "you said: " + text
	switch {
	case strings.Contains(text, "slow"):
		a.update(session, "agent_message_chunk", "working")
		select {
		case <-cancel:
			reply(map[string]string{"stopReason": "cancelled"}, nil)
		case <-time.After(30 * time.Second):
			reply(map[string]string{"stopReason": "end_turn"}, nil)
		}
		return
	case strings.Contains(text, "permission"):
		var result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		err := a.conn.Call("session/request_permission", map[string]any{
			"sessionId": session,
			"toolCall":  map[string]any{"toolCallId": "call-1", "title": "write x.txt", "kind": "edit", "status": "pending"},
			"options": []acp.PermissionOption{
				{OptionID: "yes", Name: "Allow", Kind: "allow_once"},
				{OptionID: "no", Name: "Reject", Kind: "reject_once"},
			},
		}, &result)
		switch {
		case err != nil:
			answer = "permission failed: " + err.Error()
		case result.Outcome.Outcome == "selected":
			answer = "permission: " + result.Outcome.OptionID
		default:
			answer = "permission: " + result.Outcome.Outcome
		}
	}
	half := len(answer) / 2
	a.update(session, "agent_message_chunk", answer[:half])
	a.update(session, "agent_message_chunk", answer[half:])
	if err := a.save(session, append(history, turn{User: text, Agent: answer})); err != nil {
		reply(nil, &acp.Error{Code: acp.CodeInternal, Message: err.Error()})
		return
	}
	reply(map[string]string{"stopReason": "end_turn"}, nil)
}

func (a *Agent) update(session, kind, text string) {
	_ = a.conn.Notify("session/update", map[string]any{
		"sessionId": session,
		"update":    map[string]any{"sessionUpdate": kind, "content": acp.ContentBlock{Type: "text", Text: text}},
	})
}

func (a *Agent) path(session string) string {
	return filepath.Join(a.Dir, strings.ReplaceAll(filepath.Base(session), ".", "_")+".json")
}

func (a *Agent) save(session string, history []turn) error {
	raw, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return os.WriteFile(a.path(session), raw, 0o600)
}

func (a *Agent) load(session string) ([]turn, error) {
	raw, err := os.ReadFile(a.path(session))
	if err != nil {
		return nil, err
	}
	var history []turn
	return history, json.Unmarshal(raw, &history)
}
