package connect

import (
	"crypto/ed25519"
	"net/http"
	"regexp"
	"time"

	"dyna-agent/internal/websvc"
)

// --- hello ---

var nonceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// HelloMessage is what a hello signs. A page checks it with the machine
// key it pinned from the relay.
func HelloMessage(machine, origin, nonce string) string {
	return "dyna-connect-hello.v1\n" + machine + "\n" + origin + "\n" + nonce
}

func (s *Service) hello(w http.ResponseWriter, r *http.Request, rt websvc.Route) {
	h := w.Header()
	// Any page may ask; the answer carries no secret and no credentials.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		websvc.JSONError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	l, ok := s.link()
	if !ok {
		websvc.JSONError(w, http.StatusNotFound, "this machine is not linked")
		return
	}
	if !s.limit.Allow("hello", 120, time.Minute) {
		websvc.JSONError(w, http.StatusTooManyRequests, "too many hellos")
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if !nonceRe.MatchString(nonce) {
		websvc.JSONError(w, http.StatusBadRequest, "nonce must be 16 to 64 base64url characters")
		return
	}
	sig := ed25519.Sign(s.key, []byte(HelloMessage(l.Machine, rt.App, nonce)))
	websvc.JSONReply(w, http.StatusOK, map[string]string{"machine": l.Machine, "origin": rt.App, "nonce": nonce, "sig": b64.EncodeToString(sig)})
}
