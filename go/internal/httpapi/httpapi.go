// Package httpapi serves the client and its backend API.
//
// The route map mirrors what @osjs/client requests: /ping, /login, /logout,
// /settings and /vfs/<method>. It is frozen -- not ours to change -- and the
// websocket at / is the one extension point it leaves open.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
}

func New(settings config.Config, files *vfs.Vfs, registry *socket.Registry, handler *chat.Handler) *Server {
	sockets, closing := context.WithCancel(context.Background())
	return &Server{
		settings: settings, files: files, registry: registry, chat: handler,
		sockets: sockets, closing: closing,
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
		next.ServeHTTP(w, r)
	})
}

// require returns the caller's profile, answering 403 when there is none.
func (s *Server) require(w http.ResponseWriter, r *http.Request) (socket.Profile, bool) {
	profile, ok := s.session(r)
	if !ok {
		fail(w, http.StatusForbidden, "Not authenticated")
		return profile, false
	}
	// Refreshed on every request, which is what makes the lifetime rolling.
	s.issue(w, profile)
	return profile, true
}

// -- routes ------------------------------------------------------------------

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&credentials)

	password, known := s.settings.Users[credentials.Username]
	if !known || password != credentials.Password {
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
	s.issue(w, profile)
	write(w, http.StatusOK, profile)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clear(w)
	write(w, http.StatusOK, map[string]any{})
}

func (s *Server) readSettings(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.require(w, r)
	if !ok {
		return
	}

	target, err := s.files.Resolve(settingsPath, profile.Username, "writefile")
	if err != nil {
		report(w, err)
		return
	}

	raw, err := os.ReadFile(target)
	if err != nil || !json.Valid(raw) {
		write(w, http.StatusOK, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) writeSettings(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.require(w, r)
	if !ok {
		return
	}

	target, err := s.files.Resolve(settingsPath, profile.Username, "writefile")
	if err != nil {
		report(w, err)
		return
	}

	// The file is a flat object of namespaces, and a client merges into it
	// precisely so one does not drop another's keys. A payload of any other
	// shape would destroy them, so it is refused rather than stored.
	raw, err := io.ReadAll(r.Body)
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

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fail(w, http.StatusBadRequest, "Could not store the settings")
		return
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		fail(w, http.StatusBadRequest, "Could not store the settings")
		return
	}
	write(w, http.StatusOK, true)
}

// root serves the built client, and the websocket that shares its path.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.serveSocket(w, r)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
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
func read(r *http.Request, method string) fields {
	if !getMethods[method] {
		var body fields
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body
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
	return body
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

	body := read(r, method)
	if method == "readfile" {
		s.download(w, r, username, body)
		return
	}

	var result any
	var err error
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
	target, err := s.files.Readfile(username, body.Path)
	if err != nil {
		report(w, err)
		return
	}

	handle, err := os.Open(target)
	if err != nil {
		fail(w, http.StatusNotFound, "No such file: "+body.Path)
		return
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		fail(w, http.StatusNotFound, "No such file: "+body.Path)
		return
	}

	kind := vfs.GuessMime(target)
	disposition := "inline"
	if body.download() || !vfs.MayRenderInline(kind) {
		disposition = "attachment"
	}

	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("%s; filename=%q", disposition, filepath.Base(target)))
	http.ServeContent(w, r, filepath.Base(target), info.ModTime(), handle)
}

// announce reports a filesystem change on the system channel. Only a mutation
// that succeeded gets here.
func (s *Server) announce(method, username, path string) {
	verb, worth := announced[method]
	if !worth || path == "" {
		return
	}
	s.chat.PublishSystemEvent(fmt.Sprintf("%s %s %s", username, verb, path))
}

// -- the socket --------------------------------------------------------------

func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request) {
	// The upgrade is not a browser fetch and carries no meaningful Origin, so
	// the check that would reject it is not the one protecting this route: the
	// session cookie below is.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}

	profile, ok := s.session(r)
	if !ok {
		_ = ws.Close(websocket.StatusPolicyViolation, "Not authenticated")
		return
	}

	ctx, cancel := context.WithCancel(s.sockets)
	defer cancel()
	defer ws.CloseNow()

	service := s.chat.Service()
	presence := service.Store().Arrive(profile.Username)
	service.AnnouncePresence(profile.Username, true)

	defer func() {
		service.Store().Depart(presence)
		service.AnnouncePresence(profile.Username, false)
	}()

	maxAge := config.SessionLifetime.Milliseconds()
	s.registry.Serve(ctx, ws, profile, s.settings.Ping, maxAge, s.chat.Disconnect)
}
