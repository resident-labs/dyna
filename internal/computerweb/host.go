// Package computerweb connects the Dyna app (desktop window and web server)
// to the shared computers daemon. It turns daemon status into the view model
// the Computer screens read, opens viewer leases for the stream relay, and
// turns daemon events into app events.
//
// It never starts the daemon or a compositor. A missing daemon means every
// session shows "No desktop yet".
package computerweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/websvc"
)

// Session is a room member that can own a computer.
type Session struct {
	ID, Name string
}

// App is one window of a computer.
type App struct {
	AppID string `json:"appId"`
	Title string `json:"title"`
}

// Info is the view model of one session's computer. State is "none" (no
// computer yet), "idle" (registered, no compositor), "running" (labwc is up)
// "starting" (labwc is coming up), or "stopped" (stopped after idle or on request; last picture kept).
type Info struct {
	LatestStatus *computers.ActionIntent `json:"latestStatus,omitempty"`
	Session      string                  `json:"session"`
	Name         string                  `json:"name"`
	Room         string                  `json:"room,omitempty"`
	State        string                  `json:"state"`
	Reason       string                  `json:"reason,omitempty"`
	Width        int                     `json:"width,omitempty"`
	Height       int                     `json:"height,omitempty"`
	Renderer     string                  `json:"renderer,omitempty"`
	Apps         []App                   `json:"apps"`
	Control      string                  `json:"control,omitempty"`
	Viewers      int                     `json:"viewers"`
	Stream       computers.Stream        `json:"stream"`
	IdleSeconds  int64                   `json:"idleSeconds,omitempty"`
	RSSKiB       int64                   `json:"rssKiB"`
	StreamRSSKiB int64                   `json:"streamRssKiB"`
	StartedAt    *time.Time              `json:"startedAt,omitempty"`
	StartupMS    int64                   `json:"startupMs,omitempty"`
	// ShotAt is when last.png was taken. It is nil when there is no picture.
	ShotAt *time.Time `json:"shotAt,omitempty"`
}

// Event is what the UI gets on the app event stream. Kind is always
// "computer". Type is the daemon's event type. Computer carries the new
// status when the event has one. Action is a short line for the feed.
type Event struct {
	Kind     string `json:"kind"`
	RoomID   string `json:"roomId,omitempty"`
	Session  string `json:"session"`
	Type     string `json:"type"`
	Computer *Info  `json:"computer,omitempty"`
	Action   string `json:"action,omitempty"`
}

// Host serves the computer screens. The zero value is not usable.
type Host struct {
	// Sessions lists the sessions of a room, newest or oldest first as the
	// caller likes, including root's computer.
	Sessions func(roomID string) ([]Session, error)
	// Client opens a daemon client for a session. Tests replace it.
	Client func(session string) *computers.Client
	// Timeout bounds one daemon call. Default 4 s.
	Timeout time.Duration

	mu    sync.Mutex
	views map[string]*lease // by page key
}

type lease struct {
	session string
	l       *computers.ViewLease
}

func (h *Host) client(session string) *computers.Client {
	if h.Client != nil {
		return h.Client(session)
	}
	return computers.NewClient(session)
}

func (h *Host) ctx() (context.Context, context.CancelFunc) {
	d := h.Timeout
	if d == 0 {
		d = 4 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}

func stateOf(s computers.State) string {
	switch s {
	case computers.StateCreated:
		return "idle"
	case computers.StateRunning:
		return "running"
	case "starting": // labwc is starting; the daemon sets this state outside its lock
		return "starting"
	case computers.StateIdleStopped:
		return "stopped"
	}
	return "none"
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// FromStatus converts a daemon status.
func FromStatus(st computers.Status, name string) Info {
	latest := st.LatestStatus
	if st.Control == computers.ControlHuman && st.ControlReason != nil {
		latest = st.ControlReason
	}
	in := Info{
		LatestStatus: latest, Session: st.SessionID, Name: name, Room: st.Room, State: stateOf(st.State), Reason: st.Reason,
		Width: st.Width, Height: st.Height, Renderer: st.Renderer, Apps: []App{},
		Control: string(st.Control), Viewers: st.Viewers, Stream: st.Stream, IdleSeconds: st.IdleSeconds,
		RSSKiB: st.RSSKiB, StreamRSSKiB: st.StreamRSSKiB, StartupMS: st.StartupMS, ShotAt: timePtr(st.ShotCapturedAt),
	}
	for _, a := range st.Apps {
		in.Apps = append(in.Apps, App{AppID: a.AppID, Title: a.Title})
	}
	// The daemon keeps labwc up for a while after the last app closes, and
	// calls that "running". To the owner, a computer with no window is idle:
	// no live dot, no preview, no stream.
	if in.State == "running" && len(in.Apps) == 0 {
		in.State = "idle"
	}
	if !st.StartedAt.IsZero() {
		t := st.StartedAt
		in.StartedAt = &t
	}
	// The picture of a stopped computer may still be on disk although the
	// daemon forgot its time.
	if in.ShotAt == nil && in.State != "none" {
		if fi, err := os.Stat(lastPNG(st.SessionID)); err == nil {
			t := fi.ModTime()
			in.ShotAt = &t
		}
	}
	return in
}

func lastPNG(session string) string { return filepath.Join(computers.Dir(session), "last.png") }

// Computers lists a room's sessions. A session the daemon does not know has
// State "none". If the daemon is down, all are "none".
func (h *Host) Computers(roomID string) ([]Info, error) {
	sess, err := h.Sessions(roomID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := h.ctx()
	defer cancel()
	byID := map[string]computers.Status{}
	if list, err := h.client("").List(ctx, roomID); err == nil {
		for _, st := range list {
			byID[st.SessionID] = st
		}
	}
	// A session the room list missed (a computer registered with another room
	// field, or a list that failed) still gets its own status call.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, s := range sess {
		if _, ok := byID[s.ID]; ok {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if st, err := h.client(id).Status(ctx); err == nil && st.SessionID == id && st.State != computers.StateDestroyed {
				mu.Lock()
				byID[id] = st
				mu.Unlock()
			}
		}(s.ID)
	}
	wg.Wait()
	out := make([]Info, 0, len(sess))
	for _, s := range sess {
		if st, ok := byID[s.ID]; ok {
			out = append(out, FromStatus(st, s.Name))
		} else {
			out = append(out, Info{Session: s.ID, Name: s.Name, Room: roomID, State: "none", Apps: []App{}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].State) < rank(out[j].State) })
	return out, nil
}

func rank(state string) int {
	switch state {
	case "running", "starting":
		return 0
	case "idle":
		return 1
	case "stopped":
		return 2
	}
	return 3
}

// Live counts the computers that are running or starting, in every room. The
// machine list shows this machine's figure. A daemon that does not answer
// gives an error, so the caller shows no figure instead of zero.
func (h *Host) Live(ctx context.Context) (int, error) {
	list, err := h.client("").List(ctx, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, st := range list {
		if st.State == computers.StateRunning || st.State == computers.StateStarting {
			n++
		}
	}
	return n, nil
}

// Computer is one session's status.
func (h *Host) Computer(session string) (Info, error) {
	if err := computers.ValidateID(session); err != nil {
		return Info{}, err
	}
	ctx, cancel := h.ctx()
	defer cancel()
	st, err := h.client(session).Status(ctx)
	if err != nil {
		return Info{Session: session, State: "none", Apps: []App{}}, nil
	}
	return FromStatus(st, ""), nil
}

// Shot returns the path of the session's picture. It asks the daemon, and
// falls back to last.png on disk when the daemon is down or has no picture.
func (h *Host) Shot(session string, fresh bool) (string, error) {
	if err := computers.ValidateID(session); err != nil {
		return "", err
	}
	ctx, cancel := h.ctx()
	defer cancel()
	if sh, err := h.client(session).Shot(ctx, fresh); err == nil && sh.Path != "" {
		return sh.Path, nil
	}
	return lastPNG(session), nil
}

// OpenView opens a viewer lease and dials wayvnc. The lease stays open until
// Done runs, so the daemon sees a viewer for as long as the page streams.
func (h *Host) OpenView(session, key string) (websvc.ComputerView, error) {
	if err := computers.ValidateID(session); err != nil {
		return websvc.ComputerView{}, err
	}
	// The lease lives as long as its context: never one with a deadline.
	l, err := h.client(session).OpenView(context.Background())
	if err != nil {
		return websvc.ComputerView{}, fmt.Errorf("this computer has no stream: %w", err)
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("unix", l.RFBSocket)
	if err != nil {
		l.Close()
		return websvc.ComputerView{}, fmt.Errorf("the stream is not ready: %w", err)
	}
	h.mu.Lock()
	if h.views == nil {
		h.views = map[string]*lease{}
	}
	if old := h.views[key]; old != nil {
		old.l.Close()
	}
	mine := &lease{session: session, l: l}
	h.views[key] = mine
	h.mu.Unlock()
	return websvc.ComputerView{Conn: conn, Done: func() {
		h.mu.Lock()
		if h.views[key] == mine {
			delete(h.views, key)
		}
		h.mu.Unlock()
		l.Close()
	}}, nil
}

// Control sets who drives the computer for the viewer the page names by key.
// Human control belongs to the viewer's lease: closing the viewer, or losing
// its WebSocket, gives control back.
func (h *Host) Control(session, key, mode string) (string, error) {
	if mode != "agent" && mode != "human" {
		return "", errors.New(`mode must be "agent" or "human"`)
	}
	h.mu.Lock()
	v := h.views[key]
	h.mu.Unlock()
	if v == nil || v.session != session {
		return "", errors.New("the viewer is not open")
	}
	ctx, cancel := h.ctx()
	defer cancel()
	r, err := v.l.SetControl(ctx, computers.ControlMode(mode))
	return string(r.Control), err
}

// Watch relays daemon events to emit until ctx ends. It reconnects when the
// daemon restarts or is not up yet.
func (h *Host) Watch(ctx context.Context, emit func(Event)) {
	wait := time.Second
	for ctx.Err() == nil {
		ch, err := h.client("").Subscribe(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 15*time.Second)
			continue
		}
		wait = time.Second
		for ev := range ch {
			emit(h.event(ev))
		}
	}
}

func (h *Host) event(ev computers.Event) Event {
	out := Event{Kind: "computer", Session: ev.SessionID, Type: ev.Type}
	if ev.Status != nil {
		in := FromStatus(*ev.Status, "")
		out.Computer = &in
		out.RoomID = ev.Status.Room
	}
	if ev.Action != nil {
		out.Action = Summarize(ev.Action)
	}
	return out
}

// Summarize writes one short line for an action. The daemon cuts typed text
// before it sends the event. An action marked secret carries no text.
func Summarize(a computers.Action) string {
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(a[k], &s)
		return s
	}
	num := func(k string) (int, bool) {
		var n int
		return n, json.Unmarshal(a[k], &n) == nil
	}
	at := func() string {
		x, ok1 := num("x")
		y, ok2 := num("y")
		if ok1 && ok2 {
			return fmt.Sprintf(" · %d, %d", x, y)
		}
		return ""
	}
	typ := str("type")
	switch typ {
	case "click":
		if b := str("button"); b != "" && b != "left" {
			return b + " click" + at()
		}
		return "click" + at()
	case "double_click", "move":
		return strings.ReplaceAll(typ, "_", " ") + at()
	case "drag":
		return "drag"
	case "pointer_down", "pointer_up":
		return strings.ReplaceAll(typ, "_", " ") + at()
	case "scroll":
		// The direction follows the wheel: a positive scroll_y goes down.
		dir := ""
		if y, ok := num("scroll_y"); ok && y != 0 {
			dir = map[bool]string{true: "down", false: "up"}[y > 0]
		} else if x, ok := num("scroll_x"); ok && x != 0 {
			dir = map[bool]string{true: "right", false: "left"}[x > 0]
		}
		if dir != "" {
			return "scroll · " + dir + at()
		}
		return "scroll" + at()
	case "keypress":
		var keys []string
		_ = json.Unmarshal(a["keys"], &keys)
		return "keypress · " + strings.Join(keys, "+")
	case "type":
		var secret bool
		_ = json.Unmarshal(a["secret"], &secret)
		if secret {
			return "type · (secret)"
		}
		if text := str("text"); text != "" {
			if r := []rune(text); len(r) > 40 {
				text = string(r[:40]) + "…"
			}
			return fmt.Sprintf("type · %q", text)
		}
		if n, ok := num("chars"); ok && n > 0 {
			return fmt.Sprintf("type · %d characters", n)
		}
		return "type · (hidden)"
	case "":
		return ""
	}
	return strings.ReplaceAll(typ, "_", " ")
}
