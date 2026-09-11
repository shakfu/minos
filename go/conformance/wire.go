// Package conformance holds a server to docs/wire-contract.md over HTTP and a
// websocket, and imports none of its code.
//
// wire.go is a client written from the contract rather than from any client in
// this repository: a suite built on the terminal client would agree with the
// server about anything they had both got wrong.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	ApplicationMessage = "osjs/application:socket:message"
	Application        = "Chat"
)

// Local round trips; the ceilings turn a lost frame into a failure rather than
// a hang.
const (
	ReplyTimeout     = 10 * time.Second
	PushTimeout      = 10 * time.Second
	HandshakeTimeout = 10 * time.Second
)

// Obj is a decoded JSON object.
type Obj = map[string]any

// Fataler is what a failure is reported to: a *testing.T, or the demo's panic.
type Fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Response is one HTTP answer, successful or not: a status is part of the
// contract.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	t      Fataler
}

func (r *Response) Text() string { return string(r.Body) }

// JSON is the decoded body, or nil when there is none.
func (r *Response) JSON() any {
	r.t.Helper()
	if len(r.Body) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(r.Body, &value); err != nil {
		r.t.Fatalf("body is not JSON: %v: %q", err, r.Body)
	}
	return value
}

// Refusal is the `error` field, or "" when the body is not a refusal.
func (r *Response) Refusal() string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &payload) != nil {
		return ""
	}
	return payload.Error
}

// Http is an HTTP session against one server, holding its cookie.
type Http struct {
	Base   string
	client *http.Client
	t      Fataler
}

func NewHttp(t Fataler, base string) *Http {
	jar, _ := cookiejar.New(nil)
	return &Http{
		Base:   strings.TrimRight(base, "/"),
		client: &http.Client{Jar: jar, Timeout: 15 * time.Second},
		t:      t,
	}
}

// Call makes one request and returns the response whatever its status. A
// non-nil payload is sent as JSON and replaces body.
func (h *Http) Call(method, path string, payload any, params url.Values, body []byte, header http.Header) *Response {
	h.t.Helper()
	target := h.Base + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		h.t.Fatalf("cannot build %s %s: %v", method, path, err)
	}
	for name, values := range header {
		request.Header[name] = values
	}
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("cannot encode %v: %v", payload, err)
		}
		request.Header.Set("Content-Type", "application/json")
	}
	if body != nil {
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
	}

	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("%s %s: reading the body: %v", method, path, err)
	}
	return &Response{Status: response.StatusCode, Header: response.Header, Body: raw, t: h.t}
}

func (h *Http) Ping() *Response { return h.Call("GET", "/ping", nil, nil, nil, nil) }

func (h *Http) Login(username, password string) *Response {
	return h.Call("POST", "/login", Obj{"username": username, "password": password}, nil, nil, nil)
}

func (h *Http) Logout() *Response { return h.Call("POST", "/logout", Obj{}, nil, nil, nil) }

func (h *Http) Settings() *Response { return h.Call("GET", "/settings", nil, nil, nil, nil) }

func (h *Http) PutSettings(payload any) *Response {
	return h.Call("POST", "/settings", payload, nil, nil, nil)
}

// Methods the contract sends as GET with query parameters.
var getMethods = map[string]bool{
	"capabilities": true, "exists": true, "stat": true, "readdir": true, "readfile": true,
}

// Vfs sends a VFS call the way the contract says that method is sent.
func (h *Http) Vfs(method string, fields Obj) *Response {
	h.t.Helper()
	if !getMethods[method] {
		return h.Call("POST", "/vfs/"+method, fields, nil, nil, nil)
	}
	params := url.Values{}
	for key, value := range fields {
		switch value := value.(type) {
		case nil:
		case string:
			params.Set(key, value)
		default:
			if key != "options" {
				params.Set(key, fmt.Sprint(value))
				continue
			}
			raw, err := json.Marshal(value)
			if err != nil {
				h.t.Fatalf("cannot encode options %v: %v", value, err)
			}
			params.Set(key, string(raw))
		}
	}
	return h.Call("GET", "/vfs/"+method, nil, params, nil, nil)
}

// Upload is `writefile`, the one multipart route.
func (h *Http) Upload(path string, data []byte) *Response {
	h.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("path", path)
	part, _ := form.CreateFormFile("upload", "upload")
	_, _ = part.Write(data)
	_ = form.Close()
	header := http.Header{"Content-Type": {form.FormDataContentType()}}
	return h.Call("POST", "/vfs/writefile", nil, nil, body.Bytes(), header)
}

// CookieHeader is the session cookie, formatted for the websocket upgrade.
func (h *Http) CookieHeader() string {
	base, _ := url.Parse(h.Base)
	var pairs []string
	for _, cookie := range h.client.Jar.Cookies(base) {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(pairs, "; ")
}

// queue is unbounded, so the reader never blocks on a test that is not reading.
type queue struct {
	mu    sync.Mutex
	items []any
	ready chan struct{}
}

func newQueue() *queue { return &queue{ready: make(chan struct{}, 1)} }

func (q *queue) put(item any) {
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *queue) get(deadline time.Time) (any, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-q.ready:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Empty reports whether nothing is queued.
func (q *queue) Empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) == 0
}

func (q *queue) clear() {
	q.mu.Lock()
	q.items = nil
	q.mu.Unlock()
}

// Pred matches a push or a control frame. Anything not a JSON object never
// matches.
type Pred func(Obj) bool

type slot struct {
	done  chan struct{}
	reply any
}

// Socket is one websocket, with its frames sorted by a reader goroutine:
// replies by pid, chat pushes (null pid) into Pushes, everything else -- the
// handshake and the keepalive -- into Control.
type Socket struct {
	Pushes  *queue
	Control *queue

	// Closed is closed when the connection ends; CloseCode and CloseReason are
	// set first if the server sent a close frame.
	Closed      chan struct{}
	CloseCode   websocket.StatusCode
	CloseReason string

	conn    *websocket.Conn
	t       Fataler
	cancel  context.CancelFunc
	read    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	pid     int
	replies map[int]*slot
}

// Dial opens the socket at / with the given cookie header, which may be empty.
func Dial(t Fataler, base, cookie string) *Socket {
	t.Helper()
	target := "ws" + strings.TrimPrefix(strings.TrimRight(base, "/"), "http") + "/"
	options := &websocket.DialOptions{}
	if cookie != "" {
		options.HTTPHeader = http.Header{"Cookie": {cookie}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), HandshakeTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target, options)
	if err != nil {
		t.Fatalf("cannot open %s: %v", target, err)
	}
	conn.SetReadLimit(-1)

	lifetime, stop := context.WithCancel(context.Background())
	s := &Socket{
		Pushes: newQueue(), Control: newQueue(), Closed: make(chan struct{}),
		conn: conn, t: t, cancel: stop, read: make(chan struct{}), replies: map[int]*slot{},
	}
	go s.reader(lifetime)
	return s
}

func (s *Socket) reader(ctx context.Context) {
	defer close(s.read)
	for {
		_, raw, err := s.conn.Read(ctx)
		if err != nil {
			var closing websocket.CloseError
			if errors.As(err, &closing) {
				s.CloseCode, s.CloseReason = closing.Code, closing.Reason
			}
			break
		}
		var frame any
		if json.Unmarshal(raw, &frame) == nil {
			s.sort(frame)
		}
	}
	s.once.Do(func() { close(s.Closed) })
	s.failPending()
}

func (s *Socket) sort(value any) {
	frame, _ := value.(Obj)
	if frame == nil || frame["name"] != ApplicationMessage {
		s.Control.put(value)
		return
	}
	params, _ := frame["params"].([]any)
	var envelope Obj
	if len(params) > 0 {
		envelope, _ = params[0].(Obj)
	}
	args, _ := envelope["args"].([]any)
	var first any
	if len(args) > 0 {
		first = args[0]
	}

	pid, numbered := envelope["pid"].(float64)
	if envelope["pid"] == nil || !numbered {
		s.Pushes.put(first)
		return
	}
	// Left in place: AwaitReply removes it, so a reply may arrive first.
	s.mu.Lock()
	waiting := s.replies[int(pid)]
	s.mu.Unlock()
	if waiting == nil {
		return
	}
	select {
	case <-waiting.done:
	default:
		waiting.reply = first
		close(waiting.done)
	}
}

func (s *Socket) failPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pid, waiting := range s.replies {
		select {
		case <-waiting.done:
		default:
			waiting.reply = Obj{"error": "Disconnected"}
			close(waiting.done)
		}
		delete(s.replies, pid)
	}
}

// Close hangs up and waits for the reader. Safe to call twice.
func (s *Socket) Close() {
	done := make(chan struct{})
	go func() {
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = s.conn.CloseNow()
	}
	s.cancel()
	<-s.read
}

// SendFrame puts whatever this is on the wire: a string as-is, anything else
// as JSON. For the tests about malformed input.
func (s *Socket) SendFrame(frame any) {
	s.t.Helper()
	raw, isText := frame.(string)
	if !isText {
		encoded, err := json.Marshal(frame)
		if err != nil {
			s.t.Fatalf("cannot encode %v: %v", frame, err)
		}
		raw = string(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ReplyTimeout)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
		s.t.Fatalf("cannot send %s: %v", raw, err)
	}
}

// body is {"op": op} plus alternating key, value pairs.
func body(op string, fields []any) Obj {
	result := Obj{"op": op}
	for i := 0; i+1 < len(fields); i += 2 {
		result[fields[i].(string)] = fields[i+1]
	}
	return result
}

// SendOp sends one operation and returns the pid it went out with. Fields are
// alternating key, value pairs.
func (s *Socket) SendOp(op string, fields ...any) int {
	s.t.Helper()
	s.mu.Lock()
	s.pid++
	pid := s.pid
	s.replies[pid] = &slot{done: make(chan struct{})}
	s.mu.Unlock()
	s.SendFrame(Obj{
		"name":   ApplicationMessage,
		"params": []any{Obj{"pid": pid, "name": Application, "args": []any{body(op, fields)}}},
	})
	return pid
}

func (s *Socket) AwaitReply(pid int) any {
	s.t.Helper()
	s.mu.Lock()
	waiting := s.replies[pid]
	s.mu.Unlock()
	if waiting == nil {
		s.t.Fatalf("no request outstanding for pid %d", pid)
	}
	timer := time.NewTimer(ReplyTimeout)
	defer timer.Stop()
	select {
	case <-waiting.done:
	case <-timer.C:
		s.t.Fatalf("no reply to pid %d within %v", pid, ReplyTimeout)
	}
	s.mu.Lock()
	delete(s.replies, pid)
	s.mu.Unlock()
	return waiting.reply
}

// Call is one operation that must succeed, returning its reply object.
func (s *Socket) Call(op string, fields ...any) Obj {
	s.t.Helper()
	reply := s.AwaitReply(s.SendOp(op, fields...))
	answer, isObject := reply.(Obj)
	if !isObject {
		s.t.Fatalf("%s answered %v, not an object", op, reply)
	}
	if refusal, refused := answer["error"]; refused {
		s.t.Fatalf("%s was refused: %v", op, refusal)
	}
	return answer
}

// Refuse is one operation that must be refused, returning the message.
func (s *Socket) Refuse(op string, fields ...any) string {
	s.t.Helper()
	reply := s.AwaitReply(s.SendOp(op, fields...))
	answer, _ := reply.(Obj)
	refusal, refused := answer["error"].(string)
	if !refused {
		s.t.Fatalf("%s was not refused: %v", op, reply)
	}
	return refusal
}

// Handshake is the osjs/core:connected frame, which must be the first control
// frame on the connection.
func (s *Socket) Handshake() Obj {
	s.t.Helper()
	frame, _, found := drain(s.Control, func(Obj) bool { return true }, HandshakeTimeout)
	if !found {
		s.t.Fatalf("no handshake within %v", HandshakeTimeout)
	}
	if frame["name"] != "osjs/core:connected" {
		s.t.Fatalf("first frame was %v", frame["name"])
	}
	return frame
}

// ExpectPush is the next push matching pred, discarding the ones before it.
func (s *Socket) ExpectPush(pred Pred) Obj {
	s.t.Helper()
	return s.ExpectPushWithin(pred, PushTimeout)
}

func (s *Socket) ExpectPushWithin(pred Pred, timeout time.Duration) Obj {
	s.t.Helper()
	match, seen, found := drain(s.Pushes, pred, timeout)
	if !found {
		s.t.Fatalf("no matching push in %v; saw %s", timeout, show(seen))
	}
	return match
}

// CollectPush is ExpectPush that also returns what came before the match: the
// only way to prove a push did not arrive.
func (s *Socket) CollectPush(pred Pred) (Obj, []any) {
	s.t.Helper()
	match, seen, found := drain(s.Pushes, pred, PushTimeout)
	if !found {
		s.t.Fatalf("no matching push in %v; saw %s", PushTimeout, show(seen))
	}
	return match, seen
}

func (s *Socket) ExpectControl(pred Pred, timeout time.Duration) Obj {
	s.t.Helper()
	match, seen, found := drain(s.Control, pred, timeout)
	if !found {
		s.t.Fatalf("no matching control frame in %v; saw %s", timeout, show(seen))
	}
	return match
}

// Drain discards everything queued. Use before an action whose pushes matter.
func (s *Socket) Drain() {
	s.Pushes.clear()
	s.Control.clear()
}

func drain(from *queue, pred Pred, timeout time.Duration) (Obj, []any, bool) {
	deadline := time.Now().Add(timeout)
	var seen []any
	for {
		item, ok := from.get(deadline)
		if !ok {
			return nil, seen, false
		}
		if frame, isObject := item.(Obj); isObject && pred(frame) {
			return frame, seen, true
		}
		seen = append(seen, item)
	}
}

func show(items []any) string {
	raw, _ := json.Marshal(items)
	return string(raw)
}

// PushOf matches the next push of this type.
func PushOf(kind string) Pred {
	return func(event Obj) bool { return event["type"] == kind }
}

// MessageIn matches a message pushed in one room.
func MessageIn(room any) Pred {
	return func(event Obj) bool { return event["type"] == "message" && event["room"] == room }
}
