package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// signalContext reports when the code under test first asks about its
// cancellation, so a test can queue a call and then cancel it while it waits.
type signalContext struct {
	context.Context
	asked chan struct{}
	once  sync.Once
}

func (c *signalContext) Err() error {
	c.once.Do(func() { close(c.asked) })
	return c.Context.Err()
}

// A refresh that waits behind another one and expires queued asks no provider
// and records nothing; it answers with the last published view.
func TestQueuedRefreshCancelsWithoutAskingAProvider(t *testing.T) {
	accounts, bundle, account, st := privateClaudeAccount(t)
	writeClaudeFixtureTokens(t, account.Home, "expired-access", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "fixture@example.test", time.Now().Add(-time.Hour))
	if _, err := accounts.Capture(context.Background(), account.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	refreshes, probes := 0, 0
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "platform.claude.com":
			refreshes++
			raw, _ := json.Marshal(map[string]any{"access_token": "renewed-access", "refresh_token": "renewed-grant", "expires_in": 3600})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
		case "api.anthropic.com":
			probes++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":12,"resets_at":"2030-03-17T17:46:40Z"}}`)), Header: make(http.Header)}, nil
		}
		t.Fatalf("unexpected provider host %s", r.URL.Host)
		return nil, nil
	})
	defer func() { http.DefaultTransport = old }()

	s := New(Options{Store: st})
	s.last = Snapshot{Accounts: []Account{{ID: "saved"}}, Message: "saved warning"}

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &signalContext{Context: base, asked: make(chan struct{})}

	// Another refresh is in flight, so this one waits.
	s.refreshMu.Lock()
	done := make(chan Snapshot, 1)
	go func() { done <- s.Snapshot(ctx, true) }()
	<-ctx.asked
	cancel()
	s.refreshMu.Unlock()

	got := <-done
	if !reflect.DeepEqual(got, s.last) || got.Message != "saved warning" || len(got.Accounts) != 1 || got.Accounts[0].ID != "saved" {
		t.Fatalf("queued refresh = %+v, want the last published view", got)
	}
	if refreshes != 0 || probes != 0 {
		t.Fatalf("queued refresh asked a provider: %d refreshes, %d probes", refreshes, probes)
	}
	if state, err := st.Read(); err != nil {
		t.Fatal(err)
	} else if saved := state.Find(account.ID); saved != nil && saved.Observation != nil {
		t.Fatalf("queued refresh recorded an observation: %+v", saved.Observation)
	}
}
