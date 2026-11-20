// Package acp speaks the Agent Client Protocol, version 1: JSON-RPC 2.0, one
// message per line on stdio (https://agentclientprotocol.com/protocol/v1/transports.md).
//
// Conn is either end. Client is the end minosb is: it runs an agent and
// prompts it. The agent's end is each agent's own, and go/acpconformance
// checks it.
package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// The protocol version this package speaks.
const ProtocolVersion = 1

// JSON-RPC and ACP error codes (https://agentclientprotocol.com/protocol/v1/error.md).
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeAuthRequired   = -32000
	CodeNotFound       = -32002
)

// Error is a JSON-RPC error, sent or received.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// Message is one line of the wire.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler answers what the other end starts. Both run on the reading
// goroutine, in order, so a notification is handled before a response that
// follows it is delivered.
type Handler interface {
	// Request calls reply once, then or later and from any goroutine, so a
	// request may stay pending while others arrive. A non-nil *Error is sent
	// in place of the result.
	Request(method string, params json.RawMessage, reply func(result any, failure *Error))
	Notification(method string, params json.RawMessage)
}

// ErrClosed is a call's error when the other end's output ended first.
var ErrClosed = errors.New("the connection closed")

// Conn is one end of a connection.
type Conn struct {
	w       io.Writer
	handler Handler
	// Line, when set before Serve, sees every line received, and the error
	// when one is not a JSON-RPC message.
	Line func(line []byte, err error)

	write   sync.Mutex
	mutex   sync.Mutex
	next    int64
	pending map[string]chan Message
	done    chan struct{}
	err     error
}

// NewConn writes to w and answers through h. Serve reads.
func NewConn(w io.Writer, h Handler) *Conn {
	return &Conn{w: w, handler: h, pending: map[string]chan Message{}, done: make(chan struct{})}
}

// Serve reads r until it ends, then fails every call still waiting.
func (c *Conn) Serve(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var message Message
		err := json.Unmarshal(line, &message)
		if err == nil && message.JSONRPC != "2.0" {
			err = fmt.Errorf("jsonrpc is %q, not 2.0", message.JSONRPC)
		}
		if c.Line != nil {
			c.Line(line, err)
		}
		if err != nil {
			continue
		}
		c.receive(message)
	}
	c.mutex.Lock()
	c.err = scanner.Err()
	pending := c.pending
	c.pending = map[string]chan Message{}
	c.mutex.Unlock()
	for _, waiting := range pending {
		close(waiting)
	}
	close(c.done)
}

// Done closes when Serve returns.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) receive(message Message) {
	switch {
	case message.Method != "" && message.ID != nil:
		id := message.ID
		var once sync.Once
		c.handler.Request(message.Method, message.Params, func(result any, failure *Error) {
			once.Do(func() { c.respond(id, result, failure) })
		})
	case message.Method != "":
		c.handler.Notification(message.Method, message.Params)
	case message.ID != nil:
		c.mutex.Lock()
		waiting, ok := c.pending[string(message.ID)]
		delete(c.pending, string(message.ID))
		c.mutex.Unlock()
		if ok {
			waiting <- message
		}
	}
}

func (c *Conn) respond(id json.RawMessage, result any, failure *Error) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	if failure != nil {
		reply["error"] = failure
	} else {
		if result == nil {
			result = struct{}{}
		}
		reply["result"] = result
	}
	_ = c.send(reply)
}

// Call sends a request and decodes its result into result, which may be nil.
// A JSON-RPC error comes back as *Error. It waits as long as the other end
// takes.
func (c *Conn) Call(method string, params, result any) error {
	return c.CallWithin(0, method, params, result)
}

// CallWithin is Call, failing once limit passes without an answer. Zero is no
// limit.
func (c *Conn) CallWithin(limit time.Duration, method string, params, result any) error {
	c.mutex.Lock()
	c.next++
	id := strconv.FormatInt(c.next, 10)
	waiting := make(chan Message, 1)
	c.pending[id] = waiting
	c.mutex.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		c.mutex.Lock()
		delete(c.pending, id)
		c.mutex.Unlock()
		return err
	}
	var expired <-chan time.Time
	if limit > 0 {
		timer := time.NewTimer(limit)
		defer timer.Stop()
		expired = timer.C
	}
	var reply Message
	var ok bool
	select {
	case reply, ok = <-waiting:
	case <-expired:
		c.mutex.Lock()
		delete(c.pending, id)
		c.mutex.Unlock()
		return fmt.Errorf("no answer to %s within %s", method, limit)
	}
	switch {
	case !ok:
		return ErrClosed
	case reply.Error != nil:
		return reply.Error
	case result != nil:
		return json.Unmarshal(reply.Result, result)
	}
	return nil
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// send writes one message on one line. json.Marshal escapes every newline
// inside a string, so a line never splits.
func (c *Conn) send(message any) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.write.Lock()
	defer c.write.Unlock()
	_, err = c.w.Write(append(raw, '\n'))
	return err
}
