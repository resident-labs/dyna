package websvc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dyna-agent/internal/websvc/devicestore"

	"dyna-agent/internal/hub"
)

// Each event is encoded once, however many streams and polls send it.
func TestWireCacheEncodesOnce(t *testing.T) {
	var c wireCache
	encoded := 0
	body := marshalCounter{&encoded}
	for range 3 {
		if _, err := c.get(hub.Event{Rev: 1, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if encoded != 1 {
		t.Fatalf("encoded %d times; want once", encoded)
	}
	for rev := uint64(2); rev <= 3*hub.Keep; rev++ {
		c.get(hub.Event{Rev: rev, Topic: "posts"})
	}
	if len(c.byRev) > 2*hub.Keep {
		t.Fatalf("cache holds %d encodings; want at most %d", len(c.byRev), 2*hub.Keep)
	}
}

// An event that cannot be encoded returns its error, so the stream ends or
// the poll resets and the client reloads instead of losing it silently.
func TestWireCacheReportsAnEncodeError(t *testing.T) {
	var c wireCache
	if _, err := c.get(hub.Event{Rev: 1, Body: badBody{}}); err == nil {
		t.Fatal("an unencodable body returned no error")
	}
}

type marshalCounter struct{ n *int }

func (m marshalCounter) MarshalJSON() ([]byte, error) { *m.n++; return []byte(`{"kind":"posts"}`), nil }

type badBody struct{}

func (badBody) MarshalJSON() ([]byte, error) { return nil, errors.New("bad") }

// openStream opens /api/events with a Last-Event-ID and returns its lines.
func openStream(t *testing.T, e *env, lastID string) *bufio.Scanner {
	t.Helper()
	tok := e.paired()
	srv := httptest.NewServer(e.app)
	t.Cleanup(srv.Close)
	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	r.Host = "dyna.example.ts.net"
	r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: tok})
	r.Header.Set("Last-Event-ID", lastID)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return bufio.NewScanner(resp.Body)
}

// readUntil reads stream lines until one starts with prefix, and returns
// the lines it read, without blank lines and pings.
func readUntil(t *testing.T, sc *bufio.Scanner, prefix string) []string {
	t.Helper()
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line == "event: ping" || line == "retry: 2000" {
			continue
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, prefix) {
			return lines
		}
	}
	t.Fatalf("stream ended before %q; read %q", prefix, lines)
	return nil
}

// A reconnect in the same epoch gets the events it missed, with their IDs,
// and then the live ones, with no reset (item 14).
func TestEventsResumeFromLastEventID(t *testing.T) {
	e := newEnv(t)
	for _, topic := range []string{"rooms", "pins", "members"} {
		e.hub.Publish(hub.Event{Topic: topic})
	}
	sc := openStream(t, e, "1:1")
	got := readUntil(t, sc, `data: {"kind":"members"}`)
	want := []string{"event: hello", "data: {}", "id: 1:2", `data: {"kind":"pins"}`, "id: 1:3", `data: {"kind":"members"}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("resumed stream = %q; want %q", got, want)
	}
	e.hub.Publish(hub.Event{Topic: "runs"})
	if got := readUntil(t, sc, "data: "); strings.Join(got, "\n") != "id: 1:4\n"+`data: {"kind":"runs"}` {
		t.Fatalf("live event after the replay = %q", got)
	}
}

// After an engine restart the old IDs mean nothing: the client gets one
// reset, which makes it reload once, and then live events.
func TestEventsResetAfterNewEpoch(t *testing.T) {
	e := newEnv(t)
	e.hub.Publish(hub.Event{Topic: "rooms"})
	sc := openStream(t, e, "999:5")
	got := readUntil(t, sc, "event: reset")
	if strings.Join(got, "\n") != "event: hello\ndata: {}\nid: 1:1\nevent: reset" {
		t.Fatalf("stream after a new epoch = %q; want hello and one reset at the current position", got)
	}
	e.hub.Publish(hub.Event{Topic: "pins"})
	got = readUntil(t, sc, "data: {\"kind\"")
	if strings.Join(got, "\n") != "data: {}\nid: 1:2\n"+`data: {"kind":"pins"}` {
		t.Fatalf("after the reset = %q; want only the live event", got)
	}
}

// A new stream's hello carries the position it starts from, so a later
// reconnect can resume even before the first event arrives.
func TestEventsHelloCarriesThePosition(t *testing.T) {
	e := newEnv(t)
	e.hub.Publish(hub.Event{Topic: "rooms"})
	sc := openStream(t, e, "")
	if got := readUntil(t, sc, "event: hello"); strings.Join(got, "\n") != "id: 1:1\nevent: hello" {
		t.Fatalf("new stream = %q; want hello with id 1:1", got)
	}
}

func TestEventsReportTheEngineBuild(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Version = "build-b"
	sc := openStream(t, e, "")
	readUntil(t, sc, "event: hello")
	if !sc.Scan() || sc.Text() != `data: {"version":"build-b"}` {
		t.Fatalf("hello data = %q; want the engine build", sc.Text())
	}
	w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/events/poll", cookie: e.paired()})
	var reply PollReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Version != "build-b" {
		t.Fatalf("poll version = %q; want build-b", reply.Version)
	}
}

func TestLongPollRejectsEpochlessCursor(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	// Ensure a bare zero cursor answers immediately even before its removal.
	e.hub.Publish(hub.Event{Topic: "rooms"})
	for _, after := range []string{"0", "18446744073709551615", "invalid", "1:invalid"} {
		t.Run(after, func(t *testing.T) {
			w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/events/poll?after=" + after, cookie: tok})
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "after must be epoch:seq") {
				t.Fatalf("poll cursor %q = %d %s", after, w.Code, w.Body)
			}
		})
	}
}
