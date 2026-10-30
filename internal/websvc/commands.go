package websvc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type commandRequest struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Target string `json:"target"`
}

// commands adapts the room command routes to the engine's scoped calls.
func (s *Server) commands(w http.ResponseWriter, r *http.Request, rt Route, principal Principal) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/rooms/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "commands" {
		http.NotFound(w, r)
		return
	}
	name := "Commands"
	args := []string{parts[0]}
	if len(parts) == 3 && parts[2] == "run" && r.Method == http.MethodPost {
		if err := csrf(r, rt); err != nil {
			jsonError(w, http.StatusForbidden, err.Error())
			return
		}
		var req commandRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "provide a command name, message, and optional target")
			return
		}
		name = "RunCommand"
		args = append(args, req.Name, req.Text, req.Target)
	} else if len(parts) != 2 || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	body, _ := json.Marshal(args)
	request := r.Clone(r.Context())
	request.Method = http.MethodPost
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.Header = r.Header.Clone()
	request.Header.Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && !localRequest(r) {
		// Same-origin GETs do not need a custom CSRF header. The scoped call
		// remains subject to the device grant and owner policy.
		request.Header.Set("Origin", rt.App)
		request.Header.Set("X-Dyna", "1")
	}
	s.call(w, request, rt, principal, name)
}
