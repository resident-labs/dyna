package websvc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// terminalAttach is a POST SSE stream. Unlike EventSource, fetch supplies
// the same Origin, X-Dyna and JSON checks as every owner API call. Each
// reconnect supplies a byte offset and resumes from the retained buffer.
func (s *Server) terminalAttach(w http.ResponseWriter, r *http.Request, rt Route, tok string, pr Principal) {
	if err := csrf(r, rt); err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	if !pr.Allows(ScopeTerminal) {
		jsonError(w, http.StatusForbidden, "this device cannot attach terminals")
		return
	}
	if s.o.Terminals == nil {
		jsonError(w, http.StatusNotFound, "terminals are not available")
		return
	}
	var req struct {
		ID    string `json:"id"`
		After uint64 `json:"after"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad terminal attach request")
		return
	}
	ev, wake, err := s.o.Terminals.Read(req.ID, req.After)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	ctl := http.NewResponseController(w)
	send := func(data any) error {
		// A disconnected or slow reader cannot block a host shell or retain a
		// handler forever. Unsupported deadlines in test recorders are harmless.
		_ = ctl.SetWriteDeadline(time.Now().Add(10 * time.Second))
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}
	_ = ctl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(w, "event: hello\ndata: {}\n\n"); err != nil {
		return
	}
	fl.Flush()
	if err := send(ev); err != nil || ev.Exited {
		return
	}
	after := ev.Offset
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			ev, wake, err = s.o.Terminals.Read(req.ID, after)
			if err != nil {
				return
			}
			if err := send(ev); err != nil || ev.Exited {
				return
			}
			after = ev.Offset
		case <-tick.C:
			if !s.alive(tok, pr) {
				return
			}
			_ = ctl.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(w, "event: ping\ndata: {}\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
