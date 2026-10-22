package websvc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"dyna-agent/internal/hub"
)

// events is the live stream: Server-Sent Events, one event's wire JSON per
// message. It checks the device or Connect session on every heartbeat, so
// a revoke or an ended access window also ends an open stream.
func (s *Server) events(w http.ResponseWriter, r *http.Request, tok string, pr Principal) {
	if !pr.Allows(ScopeRead) {
		jsonError(w, http.StatusForbidden, "this device cannot read events")
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
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
	// Subscribe before replaying, so no event falls between the replay and
	// the live stream. sent skips live events that the replay covered.
	events, sent, unsubscribe := s.o.Hub.Subscribe()
	defer unsubscribe()
	epoch := s.o.Hub.Epoch()
	w.WriteHeader(http.StatusOK)
	send := func(ev hub.Event) bool {
		b, err := s.wires.get(ev)
		if err != nil {
			// End the stream rather than skip the event: the client
			// reconnects and reloads, so nothing is lost silently.
			s.o.Logf("web: encode event %s rev %d: %v", ev.Topic, ev.Rev, err)
			return false
		}
		_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", ev.ID(), b)
		return err == nil
	}
	// hello proves the stream flows. A client that does not get it soon is
	// behind a proxy that holds the body, and switches to long polls.
	_, _ = io.WriteString(w, "retry: 2000\n")
	// A reconnect names the last event it got (item 14). It gets what it
	// missed from the ring, or one reset with the new position after an
	// engine restart or when it fell behind the ring; the client then
	// reloads once. A new stream's hello carries its starting position.
	last := lastEventID(r)
	if last == "" {
		_, _ = fmt.Fprintf(w, "id: %s\nevent: hello\ndata: {}\n\n", hub.Event{Epoch: epoch, Rev: sent}.ID())
	} else {
		_, _ = io.WriteString(w, "event: hello\ndata: {}\n\n")
		from, rev, ok := hub.ParseID(last)
		missed, newest, reset, _ := s.o.Hub.Since(from, rev)
		if !ok || reset {
			_, _ = fmt.Fprintf(w, "id: %s\nevent: reset\ndata: {}\n\n", hub.Event{Epoch: epoch, Rev: newest}.ID())
			missed = nil
		}
		for _, ev := range missed {
			if !send(ev) {
				return
			}
		}
		sent = max(sent, newest)
	}
	fl.Flush()
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return // dropped as too slow; the client resumes from its last ID
			}
			if ev.Rev <= sent {
				continue
			}
			if !send(ev) {
				return
			}
			fl.Flush()
		case <-tick.C:
			if !s.alive(tok, pr) {
				return
			}
			if _, err := io.WriteString(w, "event: ping\ndata: {}\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// lastEventID is the ID of the last event a reconnecting client got.
// EventSource sends it as a header when it reconnects by itself; a client
// that opens a new stream passes it as ?last=.
func lastEventID(r *http.Request) string {
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("last")
}

// PollReply is one long poll's answer. Epoch and Seq name the newest event;
// the next poll passes them back as after=epoch:seq. Reset means the client
// must reload what it shows: the engine restarted, or the client fell more
// than the hub keeps behind.
type PollReply struct {
	Epoch  int64             `json:"epoch"`
	Seq    uint64            `json:"seq"`
	Events []json.RawMessage `json:"events"`
	Reset  bool              `json:"reset,omitempty"`
}

// poll is the event stream for clients whose proxy holds a stream's body:
// GET /api/events/poll?after=<epoch>:<seq> answers with the events after
// seq, or waits up to 25 s for the next one. With no after, it answers at
// once with the current epoch and seq to start from.
func (s *Server) poll(w http.ResponseWriter, r *http.Request, pr Principal) {
	if !pr.Allows(ScopeRead) {
		jsonError(w, http.StatusForbidden, "this device cannot read events")
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	current := s.o.Hub.Epoch()
	q := r.URL.Query().Get("after")
	if q == "" {
		jsonReply(w, http.StatusOK, PollReply{Epoch: current, Seq: s.o.Hub.Rev(), Events: []json.RawMessage{}})
		return
	}
	epoch, after, ok := hub.ParseID(q)
	if !ok {
		jsonError(w, http.StatusBadRequest, "after must be epoch:seq")
		return
	}
	timer := time.NewTimer(pollWait)
	defer timer.Stop()
	for {
		evs, last, reset, wake := s.o.Hub.Since(epoch, after)
		if reset || len(evs) > 0 {
			out := PollReply{Epoch: current, Seq: last, Reset: reset, Events: make([]json.RawMessage, 0, len(evs))}
			for _, ev := range evs {
				b, err := s.wires.get(ev)
				if err != nil {
					// Reset rather than skip the event: the client reloads.
					s.o.Logf("web: encode event %s rev %d: %v", ev.Topic, ev.Rev, err)
					out = PollReply{Epoch: current, Seq: last, Reset: true, Events: []json.RawMessage{}}
					break
				}
				out.Events = append(out.Events, b)
			}
			jsonReply(w, http.StatusOK, out)
			return
		}
		select {
		case <-wake:
		case <-timer.C:
			jsonReply(w, http.StatusOK, PollReply{Epoch: current, Seq: last, Events: []json.RawMessage{}})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// wireCache encodes each event once, however many streams and polls send
// it. It keeps about the hub's ring of encodings.
type wireCache struct {
	mu    sync.Mutex
	byRev map[uint64][]byte
}

func (c *wireCache) get(ev hub.Event) ([]byte, error) {
	c.mu.Lock()
	b, ok := c.byRev[ev.Rev]
	c.mu.Unlock()
	if ok {
		return b, nil
	}
	// Encode outside the lock, so one large body does not stall the other
	// streams. Two streams may encode the same rev at once; the first wins.
	b, err := wire(ev)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if have, ok := c.byRev[ev.Rev]; ok {
		return have, nil
	}
	if c.byRev == nil {
		c.byRev = map[uint64][]byte{}
	}
	c.byRev[ev.Rev] = b
	if len(c.byRev) > 2*hub.Keep {
		for rev := range c.byRev {
			if rev+hub.Keep < ev.Rev {
				delete(c.byRev, rev)
			}
		}
	}
	return b, nil
}

// wire is the JSON a web client reads for one event: its body, or its topic
// and room when it has no body.
func wire(ev hub.Event) ([]byte, error) {
	if ev.Body != nil {
		return json.Marshal(ev.Body)
	}
	return json.Marshal(struct {
		Kind   string `json:"kind"`
		RoomID string `json:"roomId,omitempty"`
	}{ev.Topic, ev.Room})
}
