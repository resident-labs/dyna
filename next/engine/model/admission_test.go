package model

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestShortCooldownWaitConcurrentRequests(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	cfg, err := config.ParseConfigBytes([]byte("request-retry: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	manager := newCooldownTestManager(t, cfg, upstream.URL)
	ctx := coreauth.WithDynaEligibleAuth(context.Background(), []string{base64.RawURLEncoding.EncodeToString([]byte("only-codex"))})
	hint := 200 * time.Millisecond
	cool := func(d time.Duration) {
		manager.MarkResult(ctx, coreauth.Result{AuthID: "only-codex", Model: "gpt-5.1-codex", RetryAfter: &d, Error: &coreauth.Error{HTTPStatus: 429, Message: "quota"}})
	}
	cool(hint)
	auth, _ := manager.GetByID("only-codex")
	readyAt := auth.ModelStates["gpt-5.1-codex"].NextRetryAfter
	// The SDK normalizes short provider hints; use its actual cooldown deadline.
	_, err = manager.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
	if err == nil {
		t.Fatal("non-Dyna caller's cooldown policy changed")
	}
	var wg sync.WaitGroup
	for range 63 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := manager.Execute(ctx, []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
			if err != nil || string(resp.Payload) != `{"ok":true}` {
				t.Errorf("response %s: %v", resp.Payload, err)
			}
		}()
	}
	wg.Wait()
	if len(starts) != 63 {
		t.Fatalf("starts = %d", len(starts))
	}
	if starts[0].Before(readyAt) {
		t.Fatalf("request sent during cooldown: start %s, ready %s", starts[0], readyAt)
	}
	if elapsed := starts[len(starts)-1].Sub(starts[0]); elapsed < 30*time.Millisecond {
		t.Fatalf("cooldown retries have no jitter: %s", elapsed)
	}
	cool(301 * time.Second)
	start := time.Now()
	_, err = manager.Execute(ctx, []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("long cooldown: %v", err)
	}
	cool(2 * time.Second)
	canceled, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = manager.Execute(canceled, []string{"codex"}, executor.Request{Model: "gpt-5.1-codex"}, executor.Options{})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancelled cooldown: %v", err)
	}
}
