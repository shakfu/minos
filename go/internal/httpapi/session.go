package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"minos/internal/config"
	"minos/internal/socket"
)

// The cookie is signed rather than encrypted: it carries a profile the server
// issued, and the signature is what stops a client editing the `groups` it was
// given. Nothing secret travels in it.
const cookieName = "session"

type sessionPayload struct {
	Profile socket.Profile `json:"profile"`
	Expires int64          `json:"expires"`
}

func (s *Server) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.settings.Secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// issue writes the session cookie, and is also how it is refreshed: the lifetime
// runs from now on every request that carries one.
func (s *Server) issue(w http.ResponseWriter, profile socket.Profile) {
	expires := time.Now().Add(config.SessionLifetime)
	payload, err := json.Marshal(sessionPayload{Profile: profile, Expires: expires.Unix()})
	if err != nil {
		return
	}

	body := base64.RawURLEncoding.EncodeToString(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    body + "." + s.sign(payload),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(config.SessionLifetime.Seconds()),
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

// session returns the logged-in profile, or false when the request is anonymous.
func (s *Server) session(r *http.Request) (socket.Profile, bool) {
	var none socket.Profile

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
	if time.Now().Unix() > decoded.Expires {
		return none, false
	}
	return decoded.Profile, true
}
