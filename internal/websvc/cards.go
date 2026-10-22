package websvc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"dyna-agent/internal/room"
)

// CardRef is CardURL's answer.
type CardRef struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

func (s *Server) cardURL(w http.ResponseWriter, rt Route, args []json.RawMessage) {
	var roomID, id string
	if len(args) != 2 || json.Unmarshal(args[0], &roomID) != nil || json.Unmarshal(args[1], &id) != nil ||
		!room.ValidID(roomID) || !room.ValidAttachmentID(id) {
		jsonError(w, http.StatusBadRequest, "CardURL takes a room ID and an attachment ID")
		return
	}
	t, exp := s.tickets.Issue(roomID, id)
	jsonReply(w, http.StatusOK, CardRef{URL: rt.Cards + "/rich/" + roomID + "/" + id + "?t=" + t, Expires: exp})
}

// --- card origin ---

// CardHandler serves the card origin: /rich/<room>/<id>?t=<ticket> and
// nothing else.
func (s *Server) CardHandler() http.Handler {
	return s.logged("cards", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.common(w, r, s.byCard[strings.ToLower(r.Host)]) {
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "rich" || !room.ValidID(parts[1]) || !room.ValidAttachmentID(parts[2]) ||
			!s.tickets.Valid(parts[1], parts[2], r.URL.Query().Get("t")) {
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
			return
		}
		s.o.Cards.ServeHTTP(w, r)
	}))
}
