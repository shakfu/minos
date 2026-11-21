package acp

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// The shapes this package reads and writes. Fields it does not use are left
// out; a receiver ignores what it does not know.

// ContentBlock is the text block, the one every agent must take.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Info names a client or an agent.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AgentCapabilities is what an agent's initialize result says it does.
type AgentCapabilities struct {
	LoadSession bool `json:"loadSession"`
}

// InitializeResult is the agent's answer to initialize.
type InitializeResult struct {
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities *AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Info              `json:"agentInfo"`
}

// Update is the `update` of a session/update notification. Raw keeps the
// fields a caller wants beyond these.
type Update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       *ContentBlock   `json:"-"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	Raw           json.RawMessage `json:"-"`
}

// UpdateParams is a session/update notification's params.
type UpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// PermissionOption is one choice in a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionRequest is a session/request_permission request's params.
type PermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  Update             `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// The stop reasons a prompt ends with (https://agentclientprotocol.com/protocol/prompt-turn).
var StopReasons = []string{"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled"}

// ParseUpdate reads a session/update notification's update.
func ParseUpdate(raw json.RawMessage) (Update, error) {
	var update Update
	if err := json.Unmarshal(raw, &update); err != nil {
		return update, err
	}
	var content struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &content) == nil && len(content.Content) > 0 && content.Content[0] == '{' {
		var block ContentBlock
		if json.Unmarshal(content.Content, &block) == nil {
			update.Content = &block
		}
	}
	update.Raw = raw
	if update.SessionUpdate == "" {
		return update, fmt.Errorf("an update without sessionUpdate")
	}
	return update, nil
}

// Client is the end that runs an agent. It advertises no file system and no
// terminal, so the agent uses its own, and answers either with
// method-not-found.
type Client struct {
	conn *Conn
	// OnUpdate sees each session/update, in order. Set before Serve.
	OnUpdate func(session string, update Update)
	// OnPermission answers a permission request. Unset, every request is
	// cancelled, which an agent must not take as approval.
	OnPermission func(request PermissionRequest) (optionID string, selected bool)
}

// How long initialize and session/new may take. A prompt has no limit: a
// turn may work for an hour.
var SetupLimit = 30 * time.Second

// NewClient writes requests to w, the agent's stdin. Serve reads the agent's
// stdout.
func NewClient(w io.Writer) *Client {
	client := &Client{}
	client.conn = NewConn(w, client)
	return client
}

// Conn is the client's connection, for its line hook and Done.
func (c *Client) Conn() *Conn { return c.conn }

// Serve reads the agent's stdout until it ends.
func (c *Client) Serve(r io.Reader) { c.conn.Serve(r) }

// Initialize negotiates version 1 and answers what the agent said. A
// different version is refused: this client speaks no other.
func (c *Client) Initialize(info Info) (InitializeResult, error) {
	var result InitializeResult
	err := c.conn.CallWithin(SetupLimit, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": info,
	}, &result)
	if err != nil {
		return result, err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return result, fmt.Errorf("the agent speaks ACP version %d; this client speaks %d", result.ProtocolVersion, ProtocolVersion)
	}
	return result, nil
}

// NewSession starts a session in cwd, an absolute path, with no MCP servers.
func (c *Client) NewSession(cwd string) (string, error) {
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.conn.CallWithin(SetupLimit, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}}, &result); err != nil {
		return "", err
	}
	if result.SessionID == "" {
		return "", fmt.Errorf("session/new answered no sessionId")
	}
	return result.SessionID, nil
}

// Prompt runs one turn and answers its stop reason. Updates arrive through
// OnUpdate before it returns.
func (c *Client) Prompt(session, text string) (string, error) {
	var result struct {
		StopReason string `json:"stopReason"`
	}
	err := c.conn.Call("session/prompt", map[string]any{
		"sessionId": session,
		"prompt":    []ContentBlock{{Type: "text", Text: text}},
	}, &result)
	return result.StopReason, err
}

// Cancel asks the agent to end the running turn, which then stops with
// `cancelled`.
func (c *Client) Cancel(session string) error {
	return c.conn.Notify("session/cancel", map[string]string{"sessionId": session})
}

// Request answers the agent's requests: permission through OnPermission, and
// nothing else, since the client advertised nothing else.
func (c *Client) Request(method string, params json.RawMessage, reply func(any, *Error)) {
	if method != "session/request_permission" {
		reply(nil, &Error{Code: CodeMethodNotFound, Message: "Method not found: " + method})
		return
	}
	var request PermissionRequest
	if err := json.Unmarshal(params, &request); err != nil {
		reply(nil, &Error{Code: CodeInvalidParams, Message: err.Error()})
		return
	}
	outcome := map[string]any{"outcome": "cancelled"}
	if c.OnPermission != nil {
		if id, selected := c.OnPermission(request); selected {
			outcome = map[string]any{"outcome": "selected", "optionId": id}
		}
	}
	reply(map[string]any{"outcome": outcome}, nil)
}

// Notification passes session/update to OnUpdate and drops the rest.
func (c *Client) Notification(method string, params json.RawMessage) {
	if method != "session/update" || c.OnUpdate == nil {
		return
	}
	var update UpdateParams
	if json.Unmarshal(params, &update) != nil {
		return
	}
	if parsed, err := ParseUpdate(update.Update); err == nil {
		c.OnUpdate(update.SessionID, parsed)
	}
}
