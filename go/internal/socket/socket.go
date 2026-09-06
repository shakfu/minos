// Package socket is the WebSocket transport.
//
// A client opens one socket to `/` and multiplexes named messages over it as
// JSON {name, params} frames. The server pushes a handshake and keepalive pings;
// the client sends application messages back. Nothing here assumes what the
// client is.
package socket

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Inbound names are client-controlled, so only this one internal name is
// accepted. Without the guard a page could forge events such as
// osjs/core:logged-in and drive server-side handlers that trust them.
const ApplicationMessage = "osjs/application:socket:message"

// Frame names the server sends on its own account.
const (
	Connected = "osjs/core:connected"
	Ping      = "osjs/core:ping"
)

// How long one write may take before the connection is considered gone.
const writeTimeout = 10 * time.Second

// Frames a connection may fall behind by before it is hung up on.
//
// A fan-out must never be held up by one slow reader: every recipient of a
// broadcast would wait on the worst of them, and a peer that has gone away
// without closing cleanly would stall unrelated connections for a whole write
// timeout. So a send is a queue push, and a client that cannot keep up is
// disconnected rather than tolerated -- it reconnects and repairs from its
// cursor, which is what the sequence number is for.
const outboundDepth = 256

// ErrGone is returned by a send to a connection that is closed or too far
// behind. Callers drop it: the connection's own Serve loop does the cleanup.
var ErrGone = errors.New("connection is gone")

// Handler is a package's server script: one inbound application message.
type Handler func(connection *Connection, respond func(any), args []json.RawMessage)

// Profile is who a connection belongs to. The registry only ever asks for the
// username, so the rest travels as it arrived.
type Profile struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
}

// IsAdmin reads the role off the profile the session carries, rather than any
// current configuration, so a session keeps the rights it was issued with.
func (p Profile) IsAdmin() bool {
	for _, group := range p.Groups {
		if group == "admin" {
			return true
		}
	}
	return false
}

// Encode is one frame, as the wire carries it. Separate from Send so a fan-out
// can serialise once rather than once per recipient.
func Encode(name string, params []any) []byte {
	if params == nil {
		params = []any{}
	}
	raw, err := json.Marshal(struct {
		Name   string `json:"name"`
		Params []any  `json:"params"`
	}{Name: name, Params: params})
	if err != nil {
		return []byte(`{"name":"","params":[]}`)
	}
	return raw
}

// Connection is one connected client.
//
// Frames leave through a queue drained by one goroutine, which is what
// serialises them: two senders cannot interleave the halves of a message, and
// neither waits for the socket.
type Connection struct {
	ws      *websocket.Conn
	profile Profile
	ctx     context.Context

	outbound chan []byte
	done     chan struct{}
	once     sync.Once
}

func newConnection(ctx context.Context, ws *websocket.Conn, profile Profile) *Connection {
	return &Connection{
		ws: ws, profile: profile, ctx: ctx,
		outbound: make(chan []byte, outboundDepth),
		done:     make(chan struct{}),
	}
}

func (c *Connection) Profile() Profile { return c.profile }

func (c *Connection) Send(name string, params []any) error {
	return c.SendFrame(Encode(name, params))
}

// SendFrame queues an already-encoded frame. It never blocks.
func (c *Connection) SendFrame(frame []byte) error {
	select {
	case <-c.done:
		return ErrGone
	default:
	}

	select {
	case c.outbound <- frame:
		return nil
	case <-c.done:
		return ErrGone
	default:
		// The queue is full, so this peer is not reading. Hanging up is the
		// honest answer; waiting would make its problem everybody's.
		c.shutdown()
		return ErrGone
	}
}

// shutdown stops the writer and unblocks the reader. Safe to call twice.
func (c *Connection) shutdown() {
	c.once.Do(func() {
		close(c.done)
		_ = c.ws.CloseNow()
	})
}

// writer drains the queue for the life of the connection.
func (c *Connection) writer() {
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.outbound:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.shutdown()
				return
			}
		}
	}
}

// Registry is the set of live connections, the fan-out over them, and the
// handlers. Scoped to one server rather than to the package, so two servers in
// one process cannot take over each other's handlers.
type Registry struct {
	mutex       sync.Mutex
	connections map[*Connection]bool
	handlers    map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{connections: map[*Connection]bool{}, handlers: map[string]Handler{}}
}

func (r *Registry) Register(name string, handler Handler) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.handlers[name] = handler
}

func (r *Registry) add(connection *Connection) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.connections[connection] = true
}

func (r *Registry) remove(connection *Connection) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	delete(r.connections, connection)
}

func (r *Registry) Len() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.connections)
}

// Broadcast sends to every connection the predicate accepts, and reports how
// many were reached. The frame is encoded once for the whole fan-out.
func (r *Registry) Broadcast(name string, params []any, wanted func(Profile) bool) int {
	r.mutex.Lock()
	targets := make([]*Connection, 0, len(r.connections))
	for connection := range r.connections {
		if wanted == nil || wanted(connection.profile) {
			targets = append(targets, connection)
		}
	}
	r.mutex.Unlock()

	if len(targets) == 0 {
		return 0
	}

	frame := Encode(name, params)
	reached := 0
	for _, connection := range targets {
		// The peer can drop between the snapshot and the send; its own Serve
		// loop removes it from the registry.
		if err := connection.SendFrame(frame); err == nil {
			reached++
		}
	}
	return reached
}

// -- one connection ----------------------------------------------------------

type frame struct {
	Name   string            `json:"name"`
	Params []json.RawMessage `json:"params"`
}

type envelope struct {
	Pid  json.RawMessage   `json:"pid"`
	Name string            `json:"name"`
	Args []json.RawMessage `json:"args"`
}

// Serve runs one connection until the client goes away.
//
// The keepalive is a ticker rather than a read deadline: cancelling a read on
// this websocket library closes the connection, so a deadline per read would
// hang up on any client that paused.
// `released` is called once the connection is gone, so whatever a handler
// attached to it -- an occupancy, most of all -- is given up rather than held by
// a socket nobody is on the other end of.
func (r *Registry) Serve(
	ctx context.Context, ws *websocket.Conn, profile Profile,
	ping time.Duration, sessionMaxAge int64, released func(*Connection),
) {
	connection := newConnection(ctx, ws, profile)
	go connection.writer()

	r.add(connection)
	defer func() {
		r.remove(connection)
		connection.shutdown()
		if released != nil {
			released(connection)
		}
	}()

	// Sent after the connection joins the fan-out, so a client that has received
	// it cannot miss a broadcast.
	cookie := map[string]any{"cookie": map[string]any{"maxAge": sessionMaxAge}}
	if err := connection.Send(Connected, []any{cookie}); err != nil {
		return
	}

	// Reads run on their own goroutine so the keepalive below has a clock of its
	// own: cancelling a read on this library closes the connection, so a read
	// deadline would hang up on any client that merely paused.

	incoming := make(chan []byte)
	go func() {
		defer close(incoming)
		defer connection.shutdown()
		for {
			kind, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText {
				continue
			}
			select {
			case incoming <- raw:
			case <-ctx.Done():
				return
			}
		}
	}()

	quiet := time.NewTicker(ping)
	defer quiet.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-connection.done:
			return
		case raw, open := <-incoming:
			if !open {
				return
			}
			quiet.Reset(ping)
			r.dispatch(connection, raw)
		case <-quiet.C:
			if err := connection.Send(Ping, nil); err != nil {
				return
			}
		}
	}
}

// dispatch routes one inbound frame. Malformed and forged frames are dropped,
// and none of them closes the socket.
func (r *Registry) dispatch(connection *Connection, raw []byte) {
	var message frame
	if err := json.Unmarshal(raw, &message); err != nil {
		log.Print("Discarding malformed socket frame")
		return
	}

	if len(message.Name) >= 4 && message.Name[:4] == "osjs" && message.Name != ApplicationMessage {
		log.Printf("Refusing forged internal message %s", message.Name)
		return
	}
	if message.Name != ApplicationMessage {
		return
	}
	if len(message.Params) == 0 {
		return
	}

	var body envelope
	if err := json.Unmarshal(message.Params[0], &body); err != nil {
		return
	}

	r.mutex.Lock()
	handler := r.handlers[body.Name]
	r.mutex.Unlock()
	if handler == nil {
		return
	}

	pid := body.Pid
	if len(pid) == 0 {
		pid = json.RawMessage("null")
	}
	respond := func(result any) {
		_ = connection.Send(ApplicationMessage, []any{struct {
			Pid  json.RawMessage `json:"pid"`
			Args []any           `json:"args"`
		}{Pid: pid, Args: []any{result}}})
	}

	// A handler is application code reached by a client-supplied payload, and a
	// panic here would end the connection -- so one bad field would cost a
	// client its socket rather than earning an error reply. The guard belongs at
	// the dispatch point so it covers every handler.
	defer func() {
		if failure := recover(); failure != nil {
			log.Printf("Application handler %s failed: %v", body.Name, failure)
			respond(map[string]string{"error": "Request failed"})
		}
	}()
	handler(connection, respond, body.Args)
}

// Push sends an unsolicited frame to an audience: a null pid, and the
// application named so a client knows whose event it is.
func (r *Registry) Push(application string, event any, wanted func(Profile) bool) int {
	return r.Broadcast(ApplicationMessage, []any{struct {
		Pid  any    `json:"pid"`
		Name string `json:"name"`
		Args []any  `json:"args"`
	}{Pid: nil, Name: application, Args: []any{event}}}, wanted)
}
