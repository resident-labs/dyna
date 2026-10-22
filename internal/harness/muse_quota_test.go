package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
)

type museCaptureFixture struct {
	registry *accountstore.Store
	out      *engineclient.Outbox
	profiles [2]profile.Profile
	refs     [2]callspec.ExecutionRef
	mu       sync.Mutex
	seen     []callspec.MuseQuotaObservation
	logs     []string
}

func writeMuseCaptureAuth(t *testing.T, config, email string) {
	t.Helper()
	path := filepath.Join(config, "muse", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schema_version": 1, "providers": map[string]any{"meta": map[string]any{"storage": "file", "mechanism": "oauth", "access_token": "local-fixture-token", "user_email": email}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func newMuseCaptureFixture(t *testing.T, mode string) *museCaptureFixture {
	t.Helper()
	// Isolate's test-name directory can exceed the Unix socket path limit.
	socketBase := os.TempDir()
	storetest.Isolate(t)
	f := &museCaptureFixture{registry: accountstore.DefaultStore()}
	for i, email := range []string{"one@example.test", "two@example.test"} {
		config := t.TempDir()
		writeMuseCaptureAuth(t, config, email)
		f.profiles[i] = profile.Profile{Harness: profile.HarnessMuse, Env: map[string]string{"XDG_CONFIG_HOME": config, "META_API_KEY": "", "TBH_CREDENTIAL_BACKEND": "file"}}
		f.refs[i] = callspec.ExecutionRef{Session: "ses_capture", Turn: 1, AdmissionID: fmt.Sprintf("ses_capture/1/native/%d", i+1)}
		if err := f.registry.Update(func(st *accountstore.State) error {
			_, err := st.Save("muse", "", "meta:email:"+email, email, accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{}`)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp(socketBase, "mq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	listener, err := net.Listen("unix", filepath.Join(socketDir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "missing" || r.URL.Path != "/api/ObserveMuseQuota" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "the engine has no method ObserveMuseQuota"})
			return
		}
		var args []callspec.MuseQuotaObservation
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
			t.Errorf("invalid observation arguments: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, args[0])
		f.mu.Unlock()
		if mode == "refused" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "observation refused"})
			return
		}
		_, _ = w.Write([]byte("null"))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	f.out = engineclient.NewOutbox(engineclient.New(listener.Addr().String(), engineclient.RestartWait(0), engineclient.Notices(nil)), "ses_capture/1/process")
	f.out.Logf = func(format string, args ...any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logs = append(f.logs, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		f.out.Close(ctx)
		if ctx.Err() != nil {
			t.Error("capture outbox did not stop")
		}
	})
	return f
}

func (f *museCaptureFixture) flush(t *testing.T, spooled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	err := f.out.Flush(ctx)
	if spooled {
		if !errors.Is(err, engineclient.ErrSpooled) {
			t.Fatalf("Flush = %v, want durable spool result", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

func (f *museCaptureFixture) observations() []callspec.MuseQuotaObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]callspec.MuseQuotaObservation(nil), f.seen...)
}

func (f *museCaptureFixture) assertNoLocalQuota(t *testing.T) {
	t.Helper()
	st, err := f.registry.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range st.Entries {
		if entry.Observation != nil {
			t.Fatal("capture wrote the local account registry")
		}
	}
}

func museCaptureMessage() (string, time.Time) {
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	return "429 Subscription quota exhausted. Your usage window resets at " + reset.Format(time.RFC3339), reset
}

func TestMuseQuotaCaptureKeepsEachAttemptAfterCancellation(t *testing.T) {
	f := newMuseCaptureFixture(t, "ok")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	execution := f.refs[0]
	a := captureMuseQuota(ctx, f.profiles[0], execution, f.out)
	execution = f.refs[1]
	b := captureMuseQuota(t.Context(), f.profiles[1], execution, f.out)
	message, reset := museCaptureMessage()
	b("", message)
	cancel()
	a("", message)
	f.flush(t, false)
	seen := f.observations()
	if len(seen) != 2 {
		t.Fatalf("engine received %d observations, want B then delayed A", len(seen))
	}
	for i, want := range []int{1, 0} {
		if seen[i].ExecutionRef != f.refs[want] || seen[i].Identity != "meta:email:"+[]string{"one@example.test", "two@example.test"}[want] || !seen[i].ResetsAt.Equal(reset) || seen[i].ObservedAt.IsZero() {
			t.Fatalf("observation %d lost its attempt or native identity: %+v", i, seen[i])
		}
	}
	f.assertNoLocalQuota(t)
}

func TestMuseQuotaCaptureRejectsIdentitySwitchAndUnadmittedCalls(t *testing.T) {
	f := newMuseCaptureFixture(t, "ok")
	message, _ := museCaptureMessage()
	record := captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], f.out)
	writeMuseCaptureAuth(t, f.profiles[0].Env["XDG_CONFIG_HOME"], "other@example.test")
	record("", message)
	writeMuseCaptureAuth(t, f.profiles[0].Env["XDG_CONFIG_HOME"], "one@example.test")
	captureMuseQuota(t.Context(), f.profiles[0], callspec.ExecutionRef{}, f.out)("", message)
	captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], nil)("", message)
	invalid := f.refs[0]
	invalid.Run, invalid.Agent = "wf_other", 1
	captureMuseQuota(t.Context(), f.profiles[0], invalid, f.out)("", message)
	nonMuse := f.profiles[0]
	nonMuse.Harness = profile.HarnessCodex
	captureMuseQuota(t.Context(), nonMuse, f.refs[0], f.out)("", message)
	captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], f.out)("", "not a quota error")
	captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], f.out)("", "429 Subscription quota exhausted. Your usage window resets at 2000-01-01T00:00:00Z")
	f.flush(t, false)
	if len(f.observations()) != 0 {
		t.Fatal("unsafe or unadmitted capture queued an observation")
	}
	f.assertNoLocalQuota(t)
}

func TestMuseQuotaCaptureHasNoOldEngineOrRefusalFallback(t *testing.T) {
	for _, mode := range []string{"missing", "refused"} {
		t.Run(mode, func(t *testing.T) {
			f := newMuseCaptureFixture(t, mode)
			message, _ := museCaptureMessage()
			captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], f.out)("", message)
			f.flush(t, false)
			f.assertNoLocalQuota(t)
			f.mu.Lock()
			logged := strings.Join(f.logs, "\n")
			f.mu.Unlock()
			if !strings.Contains(logged, "ObserveMuseQuota") {
				t.Fatal("engine refusal was not reported by the existing outbox")
			}
		})
	}
}

func TestMuseQuotaCaptureRetainsAdmittedRefInExistingSpool(t *testing.T) {
	f := newMuseCaptureFixture(t, "ok")
	queued := make(chan []engineclient.Queued, 1)
	f.out.Spool = func(lines []engineclient.Queued) error {
		queued <- append([]engineclient.Queued(nil), lines...)
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f.out.ArmTerminal(ctx)
	message, _ := museCaptureMessage()
	captureMuseQuota(t.Context(), f.profiles[0], f.refs[0], f.out)("", message)
	f.flush(t, true)
	select {
	case lines := <-queued:
		if len(lines) != 1 || lines[0].Method != "ObserveMuseQuota" || !strings.HasPrefix(lines[0].ID, "ses_capture/1/process/") || len(lines[0].Args) != 1 {
			t.Fatalf("invalid existing-outbox spool payload: %+v", lines)
		}
		in, ok := lines[0].Args[0].(callspec.MuseQuotaObservation)
		if !ok || in.ExecutionRef != f.refs[0] || in.Identity != "meta:email:one@example.test" {
			t.Fatal("spool lost the immutable attempt reference")
		}
	default:
		t.Fatal("capture was not preserved by the existing spool")
	}
	if len(f.observations()) != 0 {
		t.Fatal("expired terminal scope sent a new engine observation")
	}
	f.assertNoLocalQuota(t)
}
