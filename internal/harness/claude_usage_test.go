package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
)

type claudeUsageCaptureFixture struct {
	home string
	out  *engineclient.Outbox
	p    profile.Profile
	ref  callspec.ExecutionRef
	mu   sync.Mutex
	seen []callspec.ClaudeUsageObservation
}

func writeClaudeUsageAuth(t *testing.T, home, uuid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"%s-access","refreshToken":"%s-refresh","expiresAt":1999999999999,"scopes":["user:inference"]}}`, uuid, uuid)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`{"oauthAccount":{"accountUuid":"%s","emailAddress":"%s@example.test"}}`, uuid, uuid)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
}

// clearClaudeEnvironment removes the Claude and Anthropic variables of the
// process that runs the test binary. A live Dyna session exports the private
// runtime home through them (CLAUDE_CONFIG_DIR, CLAUDE_SECURESTORAGE_CONFIG_DIR,
// ANTHROPIC_CONFIG_DIR), and a signed-in account there would make every
// capture look admitted. With them empty, only the fixture's own home gets
// read.
func clearClaudeEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "ANTHROPIC_") || key == "XDG_CONFIG_HOME" {
			t.Setenv(key, "")
		}
	}
}

func newClaudeUsageCaptureFixture(t *testing.T) *claudeUsageCaptureFixture {
	t.Helper()
	// Isolate's test-name directory can exceed the Unix socket path limit.
	socketBase := os.TempDir()
	storetest.Isolate(t)
	clearClaudeEnvironment(t)
	f := &claudeUsageCaptureFixture{home: t.TempDir()}
	writeClaudeUsageAuth(t, f.home, "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	f.p = profile.Profile{Harness: profile.HarnessClaudeCode, Env: map[string]string{"HOME": f.home}}
	f.ref = callspec.ExecutionRef{Session: "ses_capture", Turn: 1, AdmissionID: "ses_capture/1/native/1"}
	socketDir, err := os.MkdirTemp(socketBase, "cu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	listener, err := net.Listen("unix", filepath.Join(socketDir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ObserveClaudeUsage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var args []callspec.ClaudeUsageObservation
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
			t.Errorf("invalid observation arguments: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, args[0])
		f.mu.Unlock()
		_, _ = w.Write([]byte("null"))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	f.out = engineclient.NewOutbox(engineclient.New(listener.Addr().String(), engineclient.RestartWait(0), engineclient.Notices(nil)), "ses_capture/1/process")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		f.out.Close(ctx)
	})
	return f
}

func (f *claudeUsageCaptureFixture) flush(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := f.out.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *claudeUsageCaptureFixture) observations() []callspec.ClaudeUsageObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]callspec.ClaudeUsageObservation(nil), f.seen...)
}

const claudeRateLimitStdout = "{\"type\":\"rate_limit_event\",\"rate_limit_info\":{\"status\":\"allowed\",\"unifiedWindows\":{\"five_hour\":{\"utilization\":0.18,\"resetsAt\":1792348800},\"seven_day\":{\"utilization\":0.15,\"resetsAt\":1792890000}}}}\n"

func TestClaudeUsageCaptureSendsTheLiveReading(t *testing.T) {
	f := newClaudeUsageCaptureFixture(t)
	record := captureClaudeUsage(t.Context(), f.p, f.ref, f.out)
	record.Observe(claudeRateLimitStdout)
	record.Finish()
	f.flush(t)
	seen := f.observations()
	if len(seen) != 1 {
		t.Fatalf("engine received %d observations, want the rate_limit_event", len(seen))
	}
	one := seen[0]
	if one.Identity != "anthropic:aaaaaaaa-bbbb-cccc-dddd-000000000001" || one.ExecutionRef != f.ref || one.ObservedAt.IsZero() {
		t.Fatalf("observation lost the fixture account, attempt or time: %+v", one)
	}
	if len(one.Windows) != 2 || one.Windows[0].ID != "five_hour" || one.Windows[0].UsedPercent != 18 || one.Windows[1].ID != "seven_day" || one.Windows[1].UsedPercent != 15 {
		t.Fatalf("windows = %+v, want five_hour 18%% and seven_day 15%%", one.Windows)
	}
	if !one.Windows[0].ResetsAt.Equal(time.Unix(1792348800, 0).UTC()) {
		t.Fatalf("five_hour reset = %v", one.Windows[0].ResetsAt)
	}
}

// The observation keeps the event's own receipt time, not the process
// completion time, so a newer reading saved between the event and the end of
// the run (the five-minute active re-poll can do exactly that) still wins.
func TestClaudeUsageCaptureStampsTheEventReceiptTime(t *testing.T) {
	f := newClaudeUsageCaptureFixture(t)
	record := captureClaudeUsage(t.Context(), f.p, f.ref, f.out)
	event := time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)
	completion := event.Add(10 * time.Minute)
	clock := event
	record.now = func() time.Time { return clock }
	record.Observe(claudeRateLimitStdout)
	clock = completion
	record.Finish()
	f.flush(t)
	seen := f.observations()
	if len(seen) != 1 {
		t.Fatalf("engine received %d observations, want one", len(seen))
	}
	if !seen[0].ObservedAt.Equal(event) {
		t.Fatalf("ObservedAt = %v, want the event receipt time %v (not completion %v)", seen[0].ObservedAt, event, completion)
	}
}

// A windowless rate-limit event, or assistant output that merely mentions
// the event name, cannot restamp the newest usable windows: a probe that ran
// in between stays newer, so a near-cap account cannot become eligible again
// through old event data.
func TestClaudeUsageCaptureKeepsTheNewestWindowedEventTime(t *testing.T) {
	f := newClaudeUsageCaptureFixture(t)
	record := captureClaudeUsage(t.Context(), f.p, f.ref, f.out)
	event := time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)
	later := event.Add(10 * time.Minute)
	clock := event
	record.now = func() time.Time { return clock }
	record.Observe(claudeRateLimitStdout)
	clock = later
	record.Observe(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`)
	record.Observe(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"command","input":{"command":"echo rate_limit_event"}}]}}`)
	record.Finish()
	f.flush(t)
	seen := f.observations()
	if len(seen) != 1 {
		t.Fatalf("engine received %d observations, want one", len(seen))
	}
	if !seen[0].ObservedAt.Equal(event) {
		t.Fatalf("ObservedAt = %v, want the windowed event's time %v (not %v)", seen[0].ObservedAt, event, later)
	}
	if len(seen[0].Windows) != 2 || seen[0].Windows[0].UsedPercent != 18 {
		t.Fatalf("windows = %+v, want the windowed event's data", seen[0].Windows)
	}
}

func TestClaudeUsageCaptureRejectsUnsafeSources(t *testing.T) {
	f := newClaudeUsageCaptureFixture(t)
	record := captureClaudeUsage(t.Context(), f.p, f.ref, f.out)
	record.Observe("")
	record.Observe("{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"work\"}]}}")
	// An account switch during the attempt must not attach the reading.
	writeClaudeUsageAuth(t, f.home, "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	record.Observe(claudeRateLimitStdout)
	record.Finish()
	writeClaudeUsageAuth(t, f.home, "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	captureClaudeUsage(t.Context(), f.p, callspec.ExecutionRef{}, f.out).Finish()
	captureClaudeUsage(t.Context(), f.p, f.ref, nil).Finish()
	otherHarness := f.p
	otherHarness.Harness = profile.HarnessMuse
	captureClaudeUsage(t.Context(), otherHarness, f.ref, f.out).Finish()
	f.flush(t)
	if len(f.observations()) != 0 {
		t.Fatal("unsafe or unadmitted capture queued an observation")
	}
}
