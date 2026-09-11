// Package client talks to a minos server from outside a browser.
//
// Two connections, the same two a browser makes: an HTTP session that holds the
// cookie, and one websocket that carries every named frame. The wire format is
// docs/wire-contract.md; nothing in it assumes a browser.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ApplicationMessage is the only frame name the server accepts from a client.
const ApplicationMessage = "osjs/application:socket:message"

// How long an HTTP request, a dial, or one frame write may take.
const transportTimeout = 15 * time.Second

// The largest frame accepted. A sync or a full backfill exceeds the transport's
// 32 KiB default, and hitting it closes the socket.
const readLimit = 16 << 20

// TransportError is a request the server refused, or a connection that would not open.
type TransportError struct{ Message string }

func (e *TransportError) Error() string { return e.Message }

// Profile is the session's user, as /login returns it.
type Profile struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
}

// HTTP is a session against one server. A cookie jar rather than a header kept by
// hand: the session cookie rolls, and the jar returns whichever was set last.
type HTTP struct {
	base   string
	client *http.Client
}

func NewHTTP(base string) *HTTP {
	jar, _ := cookiejar.New(nil) // never fails without options
	return &HTTP{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Jar: jar, Timeout: transportTimeout},
	}
}

func (h *HTTP) request(method, path string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, h.base+path, body)
	if err != nil {
		return nil, &TransportError{fmt.Sprintf("Cannot reach %s: %v", h.base, err)}
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := h.client.Do(request)
	if err != nil {
		return nil, &TransportError{fmt.Sprintf("Cannot reach %s: %v", h.base, err)}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, &TransportError{fmt.Sprintf("Cannot reach %s: %v", h.base, err)}
	}

	if response.StatusCode >= 400 {
		detail := string(raw)
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &refusal) == nil && refusal.Error != "" {
			detail = refusal.Error
		}
		return nil, &TransportError{fmt.Sprintf("%d: %s", response.StatusCode, detail)}
	}
	return raw, nil
}

func (h *HTTP) Login(username, password string) (Profile, error) {
	var profile Profile
	raw, err := h.request("POST", "/login", map[string]string{"username": username, "password": password})
	if err != nil {
		return profile, err
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return profile, &TransportError{fmt.Sprintf("Unreadable login reply: %v", err)}
	}
	return profile, nil
}

func (h *HTTP) Logout() error {
	_, err := h.request("POST", "/logout", map[string]any{})
	return err
}

// Frame is one websocket message, either direction.
type Frame struct {
	Name   string            `json:"name"`
	Params []json.RawMessage `json:"params"`
}

// Socket is the core websocket, with a reader goroutine behind a callback.
//
// OnFrame runs on the reader, and deciding what is a reply and what a push is
// the caller's business. Set both callbacks before Connect.
type Socket struct {
	url    string
	client *http.Client

	OnFrame func(Frame)
	OnClose func()

	mutex   sync.Mutex
	ws      *websocket.Conn
	closing bool
}

// NewSocket addresses the websocket of the server h is logged in to. The upgrade
// is gated on the session, and the jar on h's client carries it.
func NewSocket(h *HTTP) *Socket {
	return &Socket{
		url:     websocketURL(h.base),
		client:  h.client,
		OnFrame: func(Frame) {},
		OnClose: func() {},
	}
}

func (s *Socket) Connected() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.ws != nil && !s.closing
}

func (s *Socket) Connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), transportTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, s.url, &websocket.DialOptions{HTTPClient: s.client})
	if err != nil {
		return &TransportError{fmt.Sprintf("Cannot open a socket to %s: %v", s.url, err)}
	}
	ws.SetReadLimit(readLimit)

	s.mutex.Lock()
	s.ws, s.closing = ws, false
	s.mutex.Unlock()
	go s.read(ws)
	return nil
}

func (s *Socket) read(ws *websocket.Conn) {
	for {
		_, raw, err := ws.Read(context.Background())
		if err != nil {
			break
		}
		var frame Frame
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		s.OnFrame(frame)
	}

	// A close we asked for is not news; one we did not is.
	s.mutex.Lock()
	unexpected := !s.closing
	s.closing = true
	s.mutex.Unlock()
	if unexpected {
		s.OnClose()
	}
}

func (s *Socket) Send(name string, params ...any) error {
	if params == nil {
		params = []any{}
	}
	raw, err := json.Marshal(struct {
		Name   string `json:"name"`
		Params []any  `json:"params"`
	}{name, params})
	if err != nil {
		return err
	}

	s.mutex.Lock()
	ws, live := s.ws, s.ws != nil && !s.closing
	s.mutex.Unlock()
	if !live {
		return &TransportError{"Not connected"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), transportTimeout)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
		return &TransportError{fmt.Sprintf("Cannot send: %v", err)}
	}
	return nil
}

func (s *Socket) Close() {
	s.mutex.Lock()
	ws := s.ws
	s.ws, s.closing = nil, true
	s.mutex.Unlock()
	if ws != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}
}

func websocketURL(base string) string {
	parts, err := url.Parse(base)
	if err != nil {
		return base
	}
	scheme := "ws"
	if parts.Scheme == "https" {
		scheme = "wss"
	}
	return (&url.URL{Scheme: scheme, Host: parts.Host, Path: "/"}).String()
}
