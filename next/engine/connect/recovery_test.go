package connect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelRaceState struct {
	*store.Store
	checks int
}

func (s *cancelRaceState) ConnectLinked(ctx context.Context) (bool, error) {
	s.checks++
	if s.checks == 1 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func TestCancelRaceRestartsLinkedRecovery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := &cancelRaceState{Store: st}
	engineCtx, stopEngine := context.WithCancel(context.Background())
	defer stopEngine()
	ops := NewOperations(engineCtx, files, state, 1001, 1002)
	defer ops.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	done := make(chan struct{})
	ops.done = done
	ops.cancel = func() { cancelRequest(); close(done) }
	if err := ops.Cancel(requestCtx); err == nil {
		t.Fatal("cancel accepted linked recovery")
	}
	if ops.done == nil || ops.done == done {
		t.Fatal("linked recovery was not restarted")
	}
}

func TestFailedAndInterruptedStartCanRetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer relay.Close()
	opts := LinkOptions{Name: "test", AppPort: 1001, CardPort: 1002}
	if _, err := LinkMachine(ctx, &Relay{Base: relay.URL}, files, st, opts); err == nil {
		t.Fatal("start succeeded")
	}
	a, err := st.ConnectAttempt(ctx)
	if err != nil || a.State != contract.ConnectAttemptFailed {
		t.Fatalf("after failure: %+v %v", a, err)
	}
	if err := st.SetConnectAttempt(ctx, contract.ConnectAttempt{State: contract.ConnectAttemptStarting}); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkMachine(ctx, &Relay{Base: relay.URL}, files, st, opts); err == nil {
		t.Fatal("interrupted start succeeded")
	}
	a, err = st.ConnectAttempt(ctx)
	if err != nil || a.State != contract.ConnectAttemptFailed {
		t.Fatalf("after restart: %+v %v", a, err)
	}
}

func TestCancelPreservesLinkedRecovery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":"invented"}]}`
		if r.URL.Path == "/v1/machine/tunnel-token" {
			close(entered)
			select {
			case <-release:
				body = `{"token":"invented"}`
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.Key(); err != nil {
		t.Fatal(err)
	}
	if err := files.SaveLink(Link{Relay: "https://example.invalid", Machine: "invented-machine"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeConnectLink(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ops := NewOperations(context.Background(), files, st, 1001, 1002)
	ops.relay = func(base string) *Relay { return &Relay{Base: base, Client: &http.Client{Transport: transport}} }
	defer ops.Close()
	if err := ops.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := ops.Cancel(context.Background()); err == nil {
		t.Fatal("cancel accepted a linked machine")
	}
	close(release)
	<-ops.done
	if _, err := os.Stat(filepath.Join(dir, "connect", "jwks.json")); err != nil {
		t.Fatal("linked recovery did not finish")
	}
}

func TestUnlinkFinishesAfterRelayAlreadyRevoked(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGone) }))
	defer relay.Close()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.Key(); err != nil {
		t.Fatal(err)
	}
	if err := files.SaveLink(Link{Relay: relay.URL, Machine: "invented-machine"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InitializeConnectLink(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ops := NewOperations(context.Background(), files, st, 1001, 1002)
	defer ops.Close()
	if err := ops.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	linked, err := st.ConnectLinked(context.Background())
	if err != nil || linked {
		t.Fatalf("linked=%t error=%v", linked, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "connect", "machine.key")); !os.IsNotExist(err) {
		t.Fatal("machine key remains")
	}
}

func TestRestartCompletesInterruptedUnlink(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetConnectAttempt(context.Background(), contract.ConnectAttempt{State: contract.ConnectAttemptUnlinking}); err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.Key(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops := NewOperations(context.Background(), files, st, 1001, 1002)
	defer ops.Close()
	if err := ops.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ops.done == nil {
		t.Fatal("unlink recovery did not start")
	}
	<-ops.done
	a, err := st.ConnectAttempt(context.Background())
	if err != nil || a.State != contract.ConnectAttemptCanceled {
		t.Fatalf("attempt: %+v, %v", a, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "connect", "machine.key")); !os.IsNotExist(err) {
		t.Fatal("machine key remains")
	}
}

func TestCancelRemovesPendingApproval(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body startBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if name, err := os.Hostname(); err != nil || body.Name != name {
			t.Errorf("default machine name: %q %v", body.Name, err)
		}
		json.NewEncoder(w).Encode(LinkStart{ID: "invented-id", URL: "https://example.invalid", UserCode: "invented-code", Expires: time.Now().Add(time.Minute).UnixMilli()})
	}))
	defer relay.Close()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	ops := NewOperations(context.Background(), files, st, 1001, 1002)
	defer ops.Close()
	var requestedRelay string
	ops.relay = func(base string) *Relay { requestedRelay = base; return &Relay{Base: relay.URL} }
	if err := ops.Start(context.Background(), contract.ConnectStartRequest{}); err != nil {
		t.Fatal(err)
	}
	if requestedRelay != DefaultRelay {
		t.Fatalf("default relay: %q", requestedRelay)
	}
	status, err := ops.Status(context.Background())
	if err != nil || status.Code != "invented-code" {
		t.Fatalf("status: %+v, %v", status, err)
	}
	if err := ops.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = ops.Status(context.Background())
	if err != nil || status.Attempt.State != contract.ConnectAttemptCanceled || status.Code != "" {
		t.Fatalf("canceled: %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "connect", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("pending secret remains")
	}
}

func TestPendingLinkResumesAfterRestart(t *testing.T) {
	now := time.Unix(1700000000, 0)
	starts := 0
	var first startBody
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/link/start":
			starts++
			json.NewDecoder(r.Body).Decode(&first)
			json.NewEncoder(w).Encode(LinkStart{ID: "invented-attempt", UserCode: "invented-code", URL: "https://example.invalid", Expires: now.Add(time.Minute).UnixMilli()})
		case "/v1/link/poll":
			var poll pollBody
			json.NewDecoder(r.Body).Decode(&poll)
			if poll.ID != "invented-attempt" || hashToken(poll.Secret) != first.PollHash {
				t.Error("pending attempt changed")
			}
			json.NewEncoder(w).Encode(LinkPoll{Status: "linked", Machine: MachineInfo{ID: "invented-machine", Owner: Owner{ID: "invented-owner"}, Epoch: 1, App: "https://machine.example"}})
		case "/v1/machine/tunnel-token":
			w.Write([]byte(`{"token":"invented-token"}`))
		case "/.well-known/jwks.json":
			w.Write([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":"invented"}]}`))
		}
	}))
	defer relay.Close()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	opts := LinkOptions{Name: "test", AppPort: 1001, CardPort: 1002, Ticks: make(chan time.Time), Now: func() time.Time { return now }, Started: func(LinkStart, string) { cancel() }}
	_, err = LinkMachine(ctx, &Relay{Base: relay.URL}, files, st, opts)
	if err != context.Canceled {
		t.Fatalf("first call: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "connect", "pending.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("pending file: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files, err = OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- now
	opts.Ticks, opts.Started = ticks, nil
	link, err := LinkMachine(context.Background(), &Relay{Base: relay.URL}, files, st, opts)
	if err != nil || link.Machine != "invented-machine" || starts != 1 {
		t.Fatalf("resume: %v, starts %d", err, starts)
	}
	if _, err := os.Stat(filepath.Join(dir, "connect", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("pending secret remains")
	}
}
