package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"minos/internal/config"
	"minos/internal/socket"
)

func server() *Server {
	return &Server{settings: config.Config{Secret: []byte("test")}, live: newSessions()}
}

// cookie signs a payload the way issue does, so a test can set any clock.
func cookie(s *Server, payload sessionPayload) *http.Request {
	raw, _ := json.Marshal(payload)
	request := httptest.NewRequest("GET", "/settings", nil)
	request.AddCookie(&http.Cookie{
		Name: cookieName, Value: base64.RawURLEncoding.EncodeToString(raw) + "." + s.sign(raw),
	})
	return request
}

// Used every hour, a session would otherwise roll forward forever.
func TestASessionEndsAtItsLimitHoweverOftenItIsUsed(t *testing.T) {
	s := server()
	issued := time.Now().Add(-config.SessionLimit - time.Minute)
	stale := sessionPayload{
		Profile: socket.Profile{Username: "demo"}, ID: "old",
		Issued: issued.Unix(), Expires: time.Now().Add(time.Hour).Unix(),
	}
	if _, ok := s.session(cookie(s, stale)); ok {
		t.Fatal("a session past its limit was accepted")
	}
}

func TestARefreshNeverReachesPastTheLimit(t *testing.T) {
	s := server()
	issued := time.Now().Add(-config.SessionLimit + time.Hour)
	recorder := httptest.NewRecorder()
	s.issue(recorder, sessionPayload{ID: "late", Issued: issued.Unix()})

	set := recorder.Result().Cookies()[0]
	if limit := issued.Add(config.SessionLimit); set.Expires.After(limit.Add(time.Second)) {
		t.Fatalf("refreshed to %v, past the limit %v", set.Expires, limit)
	}
}

func TestARevokedSessionIsRefused(t *testing.T) {
	s := server()
	live := sessionPayload{ID: "gone", Issued: time.Now().Unix(), Expires: time.Now().Add(time.Hour).Unix()}
	if _, ok := s.session(cookie(s, live)); !ok {
		t.Fatal("a fresh session was refused")
	}
	closed := false
	release := s.live.hold("gone", func() { closed = true })
	defer release()

	s.live.revoke("gone", time.Now().Add(time.Hour))
	if _, ok := s.session(cookie(s, live)); ok {
		t.Fatal("a revoked session was accepted")
	}
	if !closed {
		t.Fatal("the session's socket was not hung up")
	}
}
