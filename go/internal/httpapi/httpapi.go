// Package httpapi serves the client and its backend API.
//
// The route map mirrors what @osjs/client requests: /ping, /login, /logout,
// /settings and /vfs/<method>. It is frozen -- not ours to change -- and the
// websocket at / is the one extension point it leaves open.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/coder/websocket"

	"minos/internal/chat"
	"minos/internal/config"
	"minos/internal/socket"
	"minos/internal/vfs"
)

const settingsPath = "home:/.osjs/settings.json"

// Methods the client sends as GET with query parameters; the rest are JSON POSTs.
var getMethods = map[string]bool{
	"capabilities": true, "exists": true, "stat": true, "readdir": true, "readfile": true,
}

// Mutations worth announcing on the system channel. The channel is the machine
// half of the chat design: a producer the server owns, arriving in the same
// window as a conversation.
var announced = map[string]string{
	"writefile": "wrote", "mkdir": "created", "unlink": "deleted",
	"touch": "touched", "rename": "renamed", "copy": "copied",
}

// The largest upload accepted into memory before spilling to disk.
const uploadBuffer = 32 << 20

// The largest request body: an upload, and any other request.
const (
	uploadLimit = 100 << 20
	bodyLimit   = 1 << 20
)

// Server holds everything a request may need.
type Server struct {
	settings config.Config
	files    *vfs.Vfs
	registry *socket.Registry
	chat     *chat.Handler

	// The lifetime of every open websocket. Not derived from any request: the
	// upgrade ends the request that carried it, and net/http may cancel that
	// request's context once the connection is hijacked -- which would close a
	// perfectly live socket the moment it opened.
	sockets context.Context
	closing context.CancelFunc

	live *sessions
}

func New(settings config.Config, files *vfs.Vfs, registry *socket.Registry, handler *chat.Handler) *Server {
	sockets, closing := context.WithCancel(context.Background())
	return &Server{
		settings: settings, files: files, registry: registry, chat: handler,
		sockets: sockets, closing: closing, live: newSessions(),
	}
}

// Close hangs up every open websocket. The HTTP server's own shutdown does not
// reach them, because a hijacked connection is no longer one of its requests.
func (s *Server) Close() { s.closing() }

// Handler builds the route map, wrapped in the headers that apply to everything.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", s.ping)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /settings", s.readSettings)
	mux.HandleFunc("POST /settings", s.writeSettings)
	mux.HandleFunc("/vfs/{method}", s.vfsRequest)
	mux.HandleFunc("/", s.root)
	return s.secured(mux)
}

// -- responses ---------------------------------------------------------------

func write(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		raw = []byte(`{"error":"Could not encode the reply"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}

// report turns a VFS error into the status it carries, and anything else into a
// bad request.
func report(w http.ResponseWriter, err error) {
	if known, ok := vfs.AsError(err); ok {
		fail(w, known.Status, known.Message)
		return
	}
	fail(w, http.StatusBadRequest, err.Error())
}

// secured applies the headers that go on every response, uploads included.
//
// The policy is the second layer under the disposition rule in vfs: even if a
// document does get rendered from this origin, script-src 'self' means the
// script it carries inline does not run. connect-src names this request's own
// host explicitly rather than relying on 'self' to cover the websocket, because
// the socket is ws:// while the page is http://.
func (s *Server) secured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"img-src 'self' data: blob:",
			fmt.Sprintf("connect-src 'self' ws://%s wss://%s", r.Host, r.Host),
			"object-src 'none'",
			"base-uri 'self'",
			"form-action 'self'",
			"frame-ancestors 'none'",
		}, "; "))
		limit := int64(bodyLimit)
		if r.URL.Path == "/vfs/writefile" {
			limit = uploadLimit
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// saysJSON answers 415 unless a POST says it is JSON. A cross-origin page may
// send text/plain without a preflight, and the body would decode all the same.
// Checked after the session, so an anonymous request still reads as one.
func saysJSON(w http.ResponseWriter, r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "Requests must be JSON")
		return false
	}
	return true
}

// tooLarge answers 413 when reading a body stopped at its limit.
func tooLarge(w http.ResponseWriter, err error) bool {
	var limited *http.MaxBytesError
	if !errors.As(err, &limited) {
		return false
	}
	fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("A request is at most %d bytes", limited.Limit))
	return true
}

// require returns the caller's profile, answering 403 when there is none.
func (s *Server) require(w http.ResponseWriter, r *http.Request) (socket.Profile, bool) {
	session, ok := s.session(r)
	if !ok {
		fail(w, http.StatusForbidden, "Not authenticated")
		return session.Profile, false
	}
	// Refreshed on every request, which is what makes the lifetime rolling.
	s.issue(w, session)
	return session.Profile, true
}

// -- routes ------------------------------------------------------------------

// ping is the keepalive: it needs no session, and refreshes one it is given.
func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.session(r); ok {
		s.issue(w, session)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !saysJSON(w, r) {
		return
	}
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&credentials); tooLarge(w, err) {
		return
	}

	password, known := s.settings.Users[credentials.Username]
	if !known || subtle.ConstantTimeCompare([]byte(password), []byte(credentials.Password)) != 1 {
		fail(w, http.StatusForbidden, "Invalid login or permission denied")
		return
	}

	// `groups` is the frozen profile shape, and it is where this server carries
	// the one role it has. The chat layer reads it from here rather than
	// consulting config, so a session issued before a change keeps the rights it
	// was issued with.
	groups := []string{}
	if s.settings.Admins[credentials.Username] {
		groups = []string{"admin"}
	}
	profile := socket.Profile{
		ID:       credentials.Username,
		Username: credentials.Username,
		Name:     credentials.Username,
		Groups:   groups,
	}

	if err := s.files.EnsureHome(credentials.Username); err != nil {
		fail(w, http.StatusInternalServerError, "Could not prepare the home directory")
		return
	}
	s.begin(w, profile)
	write(w, http.StatusOK, profile)
}

// logout ends the session on the server as well as in the caller's cookie jar,
// so a copy of the cookie stops working and the session's sockets close.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	session, ok := s.session(r)
	if !ok {
		fail(w, http.StatusForbidden, "Not authenticated")
		return
	}
	if !saysJSON(w, r) {
		return
	}
	s.live.revoke(session.ID, session.ends())
	s.clear(w)
	write(w, http.StatusOK, map[string]any{})
}

func (s *Server) readSettings(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.require(w, r)
	if !ok {
		return
	}

	raw, err := s.files.ReadFile(profile.Username, settingsPath)
	if err != nil || !json.Valid(raw) {
		write(w, http.StatusOK, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) writeSettings(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.require(w, r)
	if !ok || !saysJSON(w, r) {
		return
	}

	// The file is a flat object of namespaces, and a client merges into it
	// precisely so one does not drop another's keys. A payload of any other
	// shape would destroy them, so it is refused rather than stored.
	raw, err := io.ReadAll(r.Body)
	if tooLarge(w, err) {
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "Settings must be a JSON object")
		return
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		fail(w, http.StatusBadRequest, "Settings must be a JSON object")
		return
	}

	if err := s.files.WriteFile(profile.Username, settingsPath, raw); err != nil {
		fail(w, http.StatusBadRequest, "Could not store the settings")
		return
	}
	write(w, http.StatusOK, true)
}

// root serves the built client, and the websocket that shares its path.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.serveSocket(w, r)
		return
	}
	index := filepath.Join(s.settings.Dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, index)
}

// -- the VFS -----------------------------------------------------------------

// fields are one VFS request's parameters, however they arrived.
type fields struct {
	Path    string          `json:"path"`
	From    string          `json:"from"`
	To      string          `json:"to"`
	Root    string          `json:"root"`
	Pattern string          `json:"pattern"`
	Options json.RawMessage `json:"options"`
}

// ensure reports whether options asked for parents to be created.
func (f fields) ensure() bool { return f.flag("ensure") }

// download reports whether options forced an attachment.
func (f fields) download() bool { return f.flag("download") }

func (f fields) flag(name string) bool {
	options := map[string]any{}
	_ = json.Unmarshal(f.Options, &options)
	value, ok := options[name].(bool)
	return ok && value
}

// read collects a request's fields from wherever that method carries them. On a
// GET the options field is JSON text, and an unparseable one means no options
// rather than an error.
func read(r *http.Request, method string) (fields, error) {
	if !getMethods[method] {
		var body fields
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			return body, err
		}
		return body, nil
	}

	query := r.URL.Query()
	body := fields{
		Path:    query.Get("path"),
		From:    query.Get("from"),
		To:      query.Get("to"),
		Root:    query.Get("root"),
		Pattern: query.Get("pattern"),
	}
	if raw := query.Get("options"); json.Valid([]byte(raw)) {
		body.Options = json.RawMessage(raw)
	}
	return body, nil
}

func (s *Server) vfsRequest(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.require(w, r)
	if !ok {
		return
	}
	method := r.PathValue("method")
	username := profile.Username

	if method == "writefile" {
		s.upload(w, r, username)
		return
	}
	if !getMethods[method] && !saysJSON(w, r) {
		return
	}

	// A body that does not decode means no fields, as it always has; one that
	// stopped at the limit is refused.
	body, err := read(r, method)
	if tooLarge(w, err) {
		return
	}
	if method == "readfile" {
		s.download(w, r, username, body)
		return
	}

	var result any
	switch method {
	case "capabilities":
		result, err = s.files.Capabilities(username, body.Path)
	case "exists":
		result, err = s.files.Exists(username, body.Path)
	case "stat":
		result, err = s.files.Stat(username, body.Path)
	case "readdir":
		result, err = s.files.Readdir(username, body.Path)
	case "mkdir":
		result, err = s.files.Mkdir(username, body.Path, body.ensure())
	case "unlink":
		result, err = s.files.Unlink(username, body.Path)
	case "touch":
		result, err = s.files.Touch(username, body.Path)
	case "copy":
		result, err = s.files.Copy(username, body.From, body.To)
	case "rename":
		result, err = s.files.Rename(username, body.From, body.To)
	case "search":
		result, err = s.files.Search(username, body.Root, body.Pattern)
	default:
		fail(w, http.StatusNotFound, "No such VFS method: "+method)
		return
	}

	if err != nil {
		report(w, err)
		return
	}

	path := body.Path
	if path == "" {
		path = body.To
	}
	s.announce(method, username, path)
	write(w, http.StatusOK, result)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, username string) {
	if err := r.ParseMultipartForm(uploadBuffer); err != nil {
		if tooLarge(w, err) {
			return
		}
		fail(w, http.StatusBadRequest, "Missing upload field")
		return
	}
	file, _, err := r.FormFile("upload")
	if err != nil {
		fail(w, http.StatusBadRequest, "Missing upload field")
		return
	}
	defer file.Close()

	path := r.FormValue("path")
	written, err := s.files.Writefile(username, path, file)
	if err != nil {
		report(w, err)
		return
	}
	s.announce("writefile", username, path)
	write(w, http.StatusOK, written)
}

// download serves a file, reporting its real type either way and varying only
// the disposition. See vfs.MayRenderInline for why.
func (s *Server) download(w http.ResponseWriter, r *http.Request, username string, body fields) {
	handle, err := s.files.Readfile(username, body.Path)
	if err != nil {
		report(w, err)
		return
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		fail(w, http.StatusNotFound, "No such file: "+body.Path)
		return
	}

	name := filepath.Base(handle.Name())
	kind := vfs.GuessMime(name)
	disposition := "inline"
	if body.download() || !vfs.MayRenderInline(kind) {
		disposition = "attachment"
	}

	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, name))
	http.ServeContent(w, r, name, info.ModTime(), handle)
}

// announce reports a filesystem change on the system channel. Only a mutation
// that succeeded gets here.
func (s *Server) announce(method, username, path string) {
	verb, worth := announced[method]
	if !worth || path == "" {
		return
	}
	// A path is the caller's text, so a newline in it could forge a second line.
	printable := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, path)
	s.chat.PublishSystemEvent(fmt.Sprintf("%s %s %s", username, verb, printable))
}

// -- the socket --------------------------------------------------------------

func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request) {
	// A browser always sends Origin on an upgrade, and the library refuses one
	// naming another host, so a page elsewhere cannot use the viewer's cookie
	// here. A client that is not a browser sends none and is let through.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	session, ok := s.session(r)
	if !ok {
		_ = ws.Close(websocket.StatusPolicyViolation, "Not authenticated")
		return
	}
	profile := session.Profile

	// The socket ends with its session: at its limit, or when it logs out.
	ctx, cancel := context.WithDeadline(s.sockets, session.ends())
	defer cancel()
	defer s.live.hold(session.ID, cancel)()
	defer ws.CloseNow()

	service := s.chat.Service()
	presence, first := service.Store().Arrive(profile.Username)
	if first {
		service.AnnouncePresence(profile.Username, true)
	}

	defer func() {
		if last := service.Store().Depart(presence); last {
			service.AnnouncePresence(profile.Username, false)
		}
	}()

	maxAge := config.SessionLifetime.Milliseconds()
	s.registry.Serve(ctx, ws, profile, s.settings.Ping, maxAge, s.chat.Disconnect)
}
