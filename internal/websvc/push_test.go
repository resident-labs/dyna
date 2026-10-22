package websvc

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/webpush"
)

func TestPairedPushWithoutConnect(t *testing.T) {
	e := newEnv(t)
	push, err := openTestPush(t, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.o.Push = push
	token := e.paired()
	w := e.do(e.app, req{url: tsApp + "/api/push/status", cookie: token})
	if w.Code != 200 || !strings.Contains(w.Body.String(), push.VAPID.PublicKey()) {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	w = e.do(e.app, req{url: tsApp + "/api/push/status"})
	if w.Code != 401 {
		t.Fatalf("unauthenticated status = %d", w.Code)
	}
	w = e.do(e.app, req{url: tsApp + "/api/push/status", cookie: token, origin: "https://other.test"})
	if w.Code != 403 {
		t.Fatalf("cross-origin status = %d", w.Code)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/test",
		"keys":     map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
		"names":    true,
	})
	w = e.do(e.app, req{url: tsApp + "/api/push/subscribe", cookie: token, body: string(body)})
	if w.Code != 200 {
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	devices, err := e.store.Devices()
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
	dev := "pd:" + devices[0].ID
	entry, ok := pushEntry(t, push, dev)
	if !ok || !entry.Names {
		t.Fatalf("subscription = %+v, %v", entry, ok)
	}
	page := e.do(e.app, req{method: "GET", url: tsApp + "/"})
	if !strings.Contains(page.Body.String(), "&#34;push&#34;:true") || e.srv.o.Ext != nil {
		t.Fatalf("push without Connect: %s", page.Body)
	}
	push.Prune(func(string) bool { return false }, nil)
	if _, ok := pushEntry(t, push, dev); ok {
		t.Fatal("revoked paired subscription survived")
	}
}

func TestPushSubscribeNeedsRead(t *testing.T) {
	e := newEnv(t)
	push, err := openTestPush(t, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.o.Push = push
	token := e.paired()
	dev, ok := e.store.Check(token)
	if !ok {
		t.Fatal("paired device missing")
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/test",
		"keys":     map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
		"names":    true,
	})
	subscribe := func() int {
		t.Helper()
		return e.do(e.app, req{url: tsApp + "/api/push/subscribe", cookie: token, body: string(body)}).Code
	}
	e.putGrant("paired", dev.ID, []string{scope.Files})
	if subscribe() != 403 {
		t.Fatal("a files-only device subscribed")
	}
	grants, err := devicegrant.Open(filepath.Dir(e.store.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Delete("paired", []string{dev.ID}); err != nil {
		t.Fatal(err)
	}
	if subscribe() != 403 {
		t.Fatal("a device with no grant subscribed")
	}
	if _, ok := pushEntry(t, push, "pd:"+dev.ID); ok {
		t.Fatal("refused subscribe stored a subscription")
	}
	e.putGrant("paired", dev.ID, []string{scope.Read})
	if subscribe() != 200 {
		t.Fatal("a reader was refused")
	}
}

func TestNotifySkipsDevicesWithoutRead(t *testing.T) {
	e := newEnv(t)
	push, err := openTestPush(t, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	token := e.paired()
	dev, ok := e.store.Check(token)
	if !ok {
		t.Fatal("paired device missing")
	}
	dataDir := filepath.Dir(e.store.Dir())
	e.putGrant("paired", dev.ID, []string{scope.Files})
	ev := Event{Kind: "question", Room: "room_20261020-120000-01234567", Post: "post1", RoomNm: "Ops", From: "Ada", Text: "secret question"}
	n := push.Notifier(func(id string) bool { return PushAllowsRead(dataDir, id, nil) })
	if n.payload(ev, webpush.Entry{DeviceID: "pd:" + dev.ID, Names: true}) != nil {
		t.Fatal("a files-only device got notification text")
	}
	if n.payload(ev, webpush.Entry{DeviceID: "pd:missing", Names: true}) != nil {
		t.Fatal("a missing grant got notification text")
	}
	e.putGrant("paired", dev.ID, []string{scope.Read})
	got := n.payload(ev, webpush.Entry{DeviceID: "pd:" + dev.ID, Names: true})
	if !strings.Contains(string(got), "secret question") || !strings.Contains(string(got), "Ada") {
		t.Fatalf("reader payload = %s", got)
	}
}

func TestPushPrunesKindsIndependently(t *testing.T) {
	push, err := openTestPush(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := webpush.Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/test", P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}
	for _, dev := range []string{"pd:phone", "rl:phone"} {
		if err := push.Store.Subscribe(dev, sub, false); err != nil {
			t.Fatal(err)
		}
	}
	push.Prune(nil, func(string) bool { return false })
	if _, ok := pushEntry(t, push, "pd:phone"); !ok {
		t.Fatal("relay pruning removed a paired device")
	}
	if _, ok := pushEntry(t, push, "rl:phone"); ok {
		t.Fatal("revoked relay subscription survived")
	}
}

func TestCallPushRespectsContentPrivacy(t *testing.T) {
	push, err := openTestPush(t, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	n := push.Notifier(func(id string) bool { return id == "allowed" })
	ev := Event{Kind: "call", Room: "room_20261020-120000-01234567", Call: "c7", Text: "CI needs your sign-in"}
	for _, names := range []bool{false, true} {
		var payload Message
		if err := json.Unmarshal(n.payload(ev, webpush.Entry{DeviceID: "allowed", Names: names}), &payload); err != nil {
			t.Fatal(err)
		}
		wantBody := "Open Dyna to answer."
		if names {
			wantBody = ev.Text
		}
		if payload.T != "call" || payload.Call != "c7" || payload.Room != ev.Room || payload.Title != "Dyna would like to speak with you" || payload.Body != wantBody {
			t.Fatalf("names=%v: %+v", names, payload)
		}
	}
	if b := n.payload(ev, webpush.Entry{DeviceID: "denied", Names: true}); b != nil {
		t.Fatal("unauthorized push received call content")
	}
}

const pushRoom = "room_20261020-120000-01234567"
const pushTopic = "2026102012000001234567"

type sentPush struct {
	message Message
	topic   string
}

type recordingSender struct{ sent []sentPush }

func (r *recordingSender) SendAll(_ context.Context, payload func(webpush.Entry) []byte, opts webpush.Options) []webpush.Result {
	var m Message
	if err := json.Unmarshal(payload(webpush.Entry{DeviceID: "pd:phone", Names: true}), &m); err != nil {
		r.sent = append(r.sent, sentPush{topic: opts.Topic})
		return nil
	}
	r.sent = append(r.sent, sentPush{message: m, topic: opts.Topic})
	return nil
}

func testNotifier(t *testing.T, now *time.Time) (*Notifier, *recordingSender) {
	t.Helper()
	push, err := openTestPush(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	push.now = func() time.Time { return *now }
	n := push.Notifier(func(string) bool { return true })
	rec := &recordingSender{}
	n.send = rec
	return n, rec
}

// A completion must not swallow a question that comes right after it.
func TestQuestionRightAfterCompletionPushes(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	n, rec := testNotifier(t, &now)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Ada"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "question", Room: pushRoom, Post: "p2", From: "Ada", Text: "[QUESTION q1] Which fix?"})
	if len(rec.sent) != 2 || rec.sent[0].message.T != "turn" || rec.sent[1].message.T != "question" {
		t.Fatalf("sent %+v, want a completion and then the question", rec.sent)
	}
	if rec.sent[0].topic != pushTopic || rec.sent[1].topic != "" {
		t.Fatalf("topics %q %q, want the completion topic and no question topic", rec.sent[0].topic, rec.sent[1].topic)
	}
	if rec.sent[1].message.Title != "Ada asked a question" || rec.sent[1].message.Body != "Which fix?" {
		t.Fatalf("question payload %+v", rec.sent[1].message)
	}
}

// Two completion pushes inside the window collapse to the first one.
func TestTwoCompletionsInsideTheWindowProduceOnePush(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	n, rec := testNotifier(t, &now)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Ada"})
	now = now.Add(19 * time.Second)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Bea"})
	if len(rec.sent) != 1 || rec.sent[0].message.T != "turn" || rec.sent[0].message.Body != "Ada finished a turn" {
		t.Fatalf("sent %+v, want one completion push", rec.sent)
	}
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Cara"})
	if len(rec.sent) != 2 || rec.sent[1].message.Body != "Cara finished a turn" {
		t.Fatalf("after the window, sent %+v", rec.sent)
	}
}

// A failure does not start the shared window, so the completion after it
// still sends and the post after that completion is coalesced. Failure text
// stays private until the device shows names.
func TestFailureIsNotThrottled(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	n, rec := testNotifier(t, &now)
	n.Notify(t.Context(), Event{Kind: "failure", Room: pushRoom, Post: "p3", From: "Ada", Text: "Ada turn 2 failed. Error: boom"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Ada"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Bea"})
	if len(rec.sent) != 2 || rec.sent[0].message.T != "failure" || rec.sent[1].message.T != "turn" {
		t.Fatalf("sent %+v, want the failure and the completion, with the post coalesced", rec.sent)
	}
	if rec.sent[0].topic != "" || rec.sent[0].message.Title != "Ada failed" || !strings.Contains(rec.sent[0].message.Body, "boom") {
		t.Fatalf("failure payload %+v topic %q", rec.sent[0].message, rec.sent[0].topic)
	}
	plain := n.payload(Event{Kind: "failure", Room: pushRoom, From: "Ada", Text: "secret boom"}, webpush.Entry{DeviceID: "pd:phone"})
	if strings.Contains(string(plain), "secret") || strings.Contains(string(plain), "Ada") {
		t.Fatalf("names-off payload leaked: %s", plain)
	}
}

// Failures from distinct sessions all send, and they do not start the window
// that ordinary posts share.
func TestFailureBurstDoesNotThrottleOrStartTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	n, rec := testNotifier(t, &now)
	for _, from := range []string{"Ada", "Bea", "workflow review"} {
		n.Notify(t.Context(), Event{Kind: "failure", Room: pushRoom, From: from, Text: from + " failed"})
		now = now.Add(time.Second)
	}
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Cara"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Dan"})
	if len(rec.sent) != 4 {
		t.Fatalf("sent %+v, want three failures and one post", rec.sent)
	}
	for i := 0; i < 3; i++ {
		if rec.sent[i].message.T != "failure" || rec.sent[i].topic != "" {
			t.Fatalf("failure %d = %+v", i, rec.sent[i])
		}
	}
	if rec.sent[3].message.T != "post" || rec.sent[3].message.Body != "New post from Cara" {
		t.Fatalf("post = %+v", rec.sent[3])
	}
}

// Two ordinary posts inside the window produce one push. A call between them
// sends and does not restart the window.
func TestTwoPostsInsideTheWindowProduceOnePush(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	n, rec := testNotifier(t, &now)
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Ada"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "call", Room: pushRoom, Call: "c1", Text: "hello"})
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Bea"})
	now = now.Add(17 * time.Second)
	n.Notify(t.Context(), Event{Kind: "turn", Room: pushRoom, From: "Cara"})
	if len(rec.sent) != 2 || rec.sent[0].message.T != "post" || rec.sent[1].message.T != "call" {
		t.Fatalf("sent %+v, want the first post and the call", rec.sent)
	}
	now = now.Add(time.Second)
	n.Notify(t.Context(), Event{Kind: "post", Room: pushRoom, From: "Dan"})
	if len(rec.sent) != 3 || rec.sent[2].message.Body != "New post from Dan" {
		t.Fatalf("after the window, sent %+v", rec.sent)
	}
}

func TestNilPushSenderLogsOnce(t *testing.T) {
	var lines []string
	push, err := openTestPush(t, nil, func(format string, _ ...any) {
		lines = append(lines, format)
	})
	if err != nil {
		t.Fatal(err)
	}
	push.Sender = nil
	n := push.Notifier(func(string) bool { return true })
	n.Notify(t.Context(), Event{Kind: "question", Room: pushRoom, From: "Ada"})
	n.Notify(t.Context(), Event{Kind: "question", Room: pushRoom, From: "Ada"})
	if len(lines) != 1 || !strings.Contains(lines[0], "sender") {
		t.Fatalf("logs %+v, want one missing-sender line", lines)
	}
}

func openTestPush(t *testing.T, audit func(string, string), logf func(string, ...any)) (*Push, error) {
	t.Helper()
	dir := t.TempDir()
	if err := webpush.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	return OpenPush(dir, audit, logf)
}

func pushEntry(t *testing.T, push *Push, device string) (webpush.Entry, bool) {
	t.Helper()
	e, ok, err := push.Store.Get(device)
	if err != nil {
		t.Fatal(err)
	}
	return e, ok
}
