package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

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
			json.NewEncoder(w).Encode(LinkPoll{Status: "linked", Machine: MachineInfo{ID: "invented-machine", Owner: Owner{ID: "invented-owner"}, Epoch: 1}})
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
