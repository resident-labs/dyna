package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/ratelimit"
	"dyna-agent/internal/room"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/webpush"
)

// Push owns browser subscriptions and room notification policy, with no relay link.
type Push struct {
	Store  *webpush.Store
	VAPID  *webpush.VAPID
	Sender *webpush.Sender
	now    func() time.Time
	audit  func(string, string)
	logf   func(string, ...any)
	limits *ratelimit.Limiter
}

// OpenPush loads the VAPID key and subscriptions. The host supplies audit logging.
func OpenPush(dataDir string, audit func(string, string), logf func(string, ...any)) (*Push, error) {
	dir := filepath.Join(dataDir, "connect", "push")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	vapid, err := webpush.LoadOrCreateVAPID(dir, webpush.DefaultContact)
	if err != nil {
		return nil, err
	}
	st, err := webpush.OpenStore(dataDir)
	if err != nil {
		return nil, err
	}
	if audit == nil {
		audit = func(string, string) {}
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &Push{Store: st, VAPID: vapid, Sender: webpush.NewSender(st, vapid), now: time.Now, audit: audit, logf: logf}
	// The limiter reads the same clock, so a test can move the window too.
	p.limits = ratelimit.New(func() time.Time { return p.now() })
	return p, nil
}

func (s *Push) allow(key string, max int) bool {
	return s.limits.Allow(key, max, time.Minute)
}

func (s *Push) serve(w http.ResponseWriter, r *http.Request, rt Route, p Principal) {
	if s == nil {
		JSONError(w, http.StatusNotFound, "push is off on this machine")
		return
	}
	if err := CheckCSRF(r, rt); err != nil {
		JSONError(w, http.StatusForbidden, err.Error())
		return
	}
	var body map[string]json.RawMessage
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err == nil && len(b) > 0 && b[0] == '{' {
		err = json.Unmarshal(b, &body)
	}
	if err != nil {
		JSONError(w, http.StatusBadRequest, "bad request body")
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/api/push/")
	if op == "subscribe" && !p.Allows(ScopeRead) {
		JSONError(w, http.StatusForbidden, "this device cannot read notifications")
		return
	}
	if op == "names" {
		var names bool
		_ = json.Unmarshal(body["names"], &names)
		if names && !p.Allows(ScopeRead) {
			JSONError(w, http.StatusForbidden, "this device cannot read notifications")
			return
		}
	}
	switch op {
	case "status", "subscribe", "names", "unsubscribe", "test":
		s.push(w, r, p, op, body)
	default:
		JSONError(w, http.StatusNotFound, "no such route")
	}
}

// Message is one push payload. Notifications include room content only
// when the device enables names.
type Message struct {
	T     string `json:"t"`
	Room  string `json:"room,omitempty"`
	Post  string `json:"post,omitempty"`
	Call  string `json:"call,omitempty"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// pushDevice is the device a principal's subscription belongs to: the
// relay device for a Connect session, so one phone on two routes gets one
// push, and the paired device otherwise.
func pushDevice(p Principal) string {
	if p.Kind == "connect" {
		return "rl:" + p.Device
	}
	return "pd:" + p.Device
}

// pushStatus is the reply to a push "status" call. The web client reads all
// three fields on every reply, so none of them are omitted.
type pushStatus struct {
	Subscribed bool   `json:"subscribed"`
	Names      bool   `json:"names"`
	Key        string `json:"key"`
}

func (s *Push) push(w http.ResponseWriter, r *http.Request, p Principal, op string, body map[string]json.RawMessage) {
	dev := pushDevice(p)
	var names bool
	_ = json.Unmarshal(body["names"], &names)
	switch op {
	case "status":
		e, ok, err := s.Store.Get(dev)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, "cannot read push subscription")
			s.logf("push: %v", err)
			return
		}
		JSONReply(w, http.StatusOK, pushStatus{Subscribed: ok, Names: ok && e.Names, Key: s.VAPID.PublicKey()})
		return
	case "subscribe":
		if !s.allow("push:"+dev, 10) {
			JSONError(w, http.StatusTooManyRequests, "too many changes")
			return
		}
		var keys struct{ P256dh, Auth string }
		_ = json.Unmarshal(body["keys"], &keys)
		var endpoint string
		_ = json.Unmarshal(body["endpoint"], &endpoint)
		sub := webpush.Subscription{Endpoint: endpoint, P256dh: keys.P256dh, Auth: keys.Auth}
		if err := s.Store.Subscribe(dev, sub, names); err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.audit("push.subscribe", dev)
	case "names":
		if err := s.Store.SetNames(dev, names); err != nil {
			JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	case "unsubscribe":
		if err := s.Store.Unsubscribe(dev); err != nil && !errors.Is(err, webpush.ErrNotFound) {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit("push.unsubscribe", dev)
	case "test":
		if !s.allow("push-test:"+dev, 5) {
			JSONError(w, http.StatusTooManyRequests, "too many tests")
			return
		}
		b, _ := json.Marshal(Message{T: "test", Title: "Dyna", Body: "Notifications work on this device."})
		if err := s.Sender.Send(r.Context(), dev, b, webpush.Options{TTL: time.Minute, Urgency: "high"}); err != nil {
			JSONError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	JSONReply(w, http.StatusOK, map[string]bool{"ok": true})
}

// Prune drops subscriptions whose paired or relay device no longer has access.
// Callers preserve entries when their device store cannot be read.
func (s *Push) Prune(paired, relay func(string) bool) {
	if s == nil {
		return
	}
	_, _ = s.Store.RemoveDevicesNotIn(func(dev string) bool {
		if id, ok := strings.CutPrefix(dev, "pd:"); ok {
			return paired == nil || paired(id)
		}
		if id, ok := strings.CutPrefix(dev, "rl:"); ok {
			return relay == nil || relay(id)
		}
		return true
	})
}

var roomIDRe = regexp.MustCompile(`^room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$`)

// Event is one room event worth a push: a post addressed to the owner by
// someone else, a session's turn completion, or a failure.
type Event struct {
	Kind   string // "post", "question", "turn", "failure" or "call"
	Room   string
	Post   string
	Call   string
	RoomNm string
	From   string
	Text   string
}

// routinePushGap is the per-room limit for ordinary posts and completions.
// Questions, failures and calls do not use it and do not start it.
const routinePushGap = 20 * time.Second

// roomPushSender delivers one push to every subscription.
type roomPushSender interface {
	SendAll(context.Context, func(webpush.Entry) []byte, webpush.Options) []webpush.Result
}

// Notifier sends pushes for room events. Ordinary posts and completions share
// one push per room per 20 seconds. Questions, failures and calls always
// send, and they do not start that window. Posts and completions share a
// topic, so an unread one replaces another. Questions and failures have no
// topic, so a completion cannot replace them.
type Notifier struct {
	s             *Push
	mayRead       func(deviceKey string) bool
	send          roomPushSender // set by tests; nil reads s.Sender at send time
	mu            sync.Mutex
	last          map[string]time.Time
	loggedMissing bool
}

// Notifier makes the push sender for room events. Nil when push is off.
// mayRead reports whether that subscription may receive room content.
// A nil func allows no device.
func (s *Push) Notifier(mayRead func(deviceKey string) bool) *Notifier {
	if s == nil {
		return nil
	}
	return &Notifier{s: s, mayRead: mayRead, last: map[string]time.Time{}}
}

// PushSession is one Connect session that can justify a relay-device push.
type PushSession struct {
	ID     string
	Device string
}

// PushAllowsRead reports whether a push subscription may receive room
// content. pd: keys are paired devices. rl: keys are relay devices, and one
// of their sessions must hold read. A missing grant or a store error refuses.
func PushAllowsRead(dataDir, deviceKey string, sessions []PushSession) bool {
	grants, err := devicegrant.Open(dataDir)
	if err != nil {
		return false
	}
	if id, ok := strings.CutPrefix(deviceKey, "pd:"); ok {
		return grantHasRead(grants, "paired", id)
	}
	if id, ok := strings.CutPrefix(deviceKey, "rl:"); ok {
		for _, sess := range sessions {
			if sess.Device == id && grantHasRead(grants, "connect", sess.ID) {
				return true
			}
		}
	}
	return false
}

func grantHasRead(grants *devicegrant.Store, kind, id string) bool {
	g, ok, err := grants.Get(kind, id)
	if err != nil || !ok {
		return false
	}
	for _, name := range g.Scopes {
		if name == scope.Read {
			return true
		}
	}
	return false
}

// Notify sends one event to every subscribed device.
func (n *Notifier) Notify(ctx context.Context, ev Event) {
	if n == nil || !roomIDRe.MatchString(ev.Room) {
		return
	}
	send := n.sender()
	if send == nil {
		n.logMissingSender()
		return
	}
	if !exemptPush(ev.Kind) && !n.admitRoutine(ev.Room) {
		return
	}
	// The topic is the room id with punctuation removed, under 32 base64url
	// characters. Questions and failures omit it so a later completion cannot
	// replace them while they wait at the push service.
	topic := ""
	if ev.Kind != "question" && ev.Kind != "failure" {
		topic = strings.NewReplacer("room_", "", "-", "").Replace(ev.Room)
	}
	res := send.SendAll(ctx, func(e webpush.Entry) []byte {
		return n.payload(ev, e)
	}, webpush.Options{TTL: 6 * time.Hour, Urgency: "normal", Topic: topic})
	for _, r := range res {
		if r.Err != nil && !errors.Is(r.Err, webpush.ErrGone) {
			n.s.logf("web: push to %s: %v", r.DeviceID, r.Err)
		}
	}
}

// exemptPush reports kinds that always send. They do not start the routine window.
func exemptPush(kind string) bool {
	return kind == "question" || kind == "failure" || kind == "call"
}

// sender is the test override, or the push service's current sender.
func (n *Notifier) sender() roomPushSender {
	if n.send != nil {
		return n.send
	}
	if n.s != nil && n.s.Sender != nil {
		return n.s.Sender
	}
	return nil
}

// logMissingSender records a missing sender once. Later pushes stay quiet.
func (n *Notifier) logMissingSender() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.loggedMissing || n.s == nil {
		return
	}
	n.loggedMissing = true
	n.s.logf("web: push sender is not set")
}

// admitRoutine is the per-room limit for ordinary posts and completions.
// The first one in the window sends. A later one is dropped. That push has
// already gone out, so its text is not rewritten.
func (n *Notifier) admitRoutine(room string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.s.now()
	if t, ok := n.last[room]; ok && now.Sub(t) < routinePushGap {
		return false
	}
	n.last[room] = now
	return true
}

// payload is the push body for one subscription. Nil means do not send.
func (n *Notifier) payload(ev Event, e webpush.Entry) []byte {
	if n.mayRead == nil || !n.mayRead(e.DeviceID) {
		return nil
	}
	m := Message{T: ev.Kind, Room: ev.Room, Post: ev.Post, Call: ev.Call}
	if ev.Kind == "call" {
		m.Title = "Dyna would like to speak with you"
		m.Body = "Open Dyna to answer."
		if e.Names {
			m.Body = ev.Text
		}
		b, _ := json.Marshal(m)
		return b
	}
	if e.Names {
		m.Title = ev.RoomNm
		switch ev.Kind {
		case "question":
			m.Title = ev.From + " asked a question"
			text := ev.Text
			if _, rest, ok := strings.Cut(text, "] "); ok {
				text = strings.SplitN(rest, "\n", 2)[0]
			}
			m.Body = room.TruncateUTF8(text, 240)
		case "turn":
			m.Body = ev.From + " finished a turn"
		case "failure":
			m.Title = ev.From + " failed"
			m.Body = room.TruncateUTF8(strings.TrimSpace(ev.Text), 240)
		default:
			m.Body = "New post from " + ev.From
		}
	}
	b, _ := json.Marshal(m)
	return b
}
