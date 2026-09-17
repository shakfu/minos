package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"minos/internal/config"
	"minos/internal/socket"
)

// The cookie is signed rather than encrypted: it carries a profile the server
// issued, and the signature is what stops a client editing the `groups` it was
// given. Nothing secret travels in it.
const cookieName = "session"

// sessionPayload is the cookie's content. Expires rolls forward on every
// request; Issued does not, and bounds the session at SessionLimit.
type sessionPayload struct {
	Profile socket.Profile `json:"profile"`
	Expires int64          `json:"expires"`
	ID      string         `json:"id"`
	Issued  int64          `json:"issued"`
}

// ends is when the session stops being accepted, however often it is used.
func (p sessionPayload) ends() time.Time {
	return time.Unix(p.Issued, 0).Add(config.SessionLimit)
}

// sessions is what a stateless cookie cannot say: which sessions were logged
// out, and which sockets each one holds open. Revocations live in memory, so a
// restart forgets them; SessionLimit still ends every session.
type sessions struct {
	mutex   sync.Mutex
	revoked map[string]time.Time
	sockets map[string]map[*context.CancelFunc]bool
}

func newSessions() *sessions {
	return &sessions{revoked: map[string]time.Time{}, sockets: map[string]map[*context.CancelFunc]bool{}}
}

func (s *sessions) isRevoked(id string) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_, revoked := s.revoked[id]
	return revoked
}

// revoke refuses a session from now on and hangs up its sockets.
func (s *sessions) revoke(id string, until time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	now := time.Now()
	for other, ends := range s.revoked {
		if ends.Before(now) {
			delete(s.revoked, other)
		}
	}
	s.revoked[id] = until
	for cancel := range s.sockets[id] {
		(*cancel)()
	}
	delete(s.sockets, id)
}

// hold records a socket under its session until the returned func is called.
func (s *sessions) hold(id string, cancel context.CancelFunc) func() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.sockets[id] == nil {
		s.sockets[id] = map[*context.CancelFunc]bool{}
	}
	key := &cancel
	s.sockets[id][key] = true
	return func() {
		s.mutex.Lock()
		defer s.mutex.Unlock()
		delete(s.sockets[id], key)
		if len(s.sockets[id]) == 0 {
			delete(s.sockets, id)
		}
	}
}

func (s *Server) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.settings.Secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// begin issues a new session for a profile.
func (s *Server) begin(w http.ResponseWriter, profile socket.Profile) {
	s.issue(w, sessionPayload{Profile: profile, ID: uuid.NewString(), Issued: time.Now().Unix()})
}

// issue writes the session cookie, and is also how it is refreshed: the lifetime
// runs from now on every request that carries one, up to the session's limit.
func (s *Server) issue(w http.ResponseWriter, session sessionPayload) {
	expires := time.Now().Add(config.SessionLifetime)
	if ends := session.ends(); ends.Before(expires) {
		expires = ends
	}
	session.Expires = expires.Unix()
	payload, err := json.Marshal(session)
	if err != nil {
		return
	}

	body := base64.RawURLEncoding.EncodeToString(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    body + "." + s.sign(payload),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// session returns the logged-in session, or false when the request is anonymous.
func (s *Server) session(r *http.Request) (sessionPayload, bool) {
	var none sessionPayload

	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return none, false
	}
	body, signature, found := strings.Cut(cookie.Value, ".")
	if !found {
		return none, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return none, false
	}
	if subtle.ConstantTimeCompare([]byte(s.sign(payload)), []byte(signature)) != 1 {
		return none, false
	}

	var decoded sessionPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return none, false
	}
	now := time.Now()
	if now.Unix() > decoded.Expires || now.After(decoded.ends()) || decoded.ID == "" {
		return none, false
	}
	if s.live.isRevoked(decoded.ID) {
		return none, false
	}
	return decoded, true
}
