package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSessionAffinitySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-affinity.state")
	a := &Auth{ID: "auth-a", Provider: "anthropic"}
	b := &Auth{ID: "auth-b", Provider: "anthropic"}
	opts := func() cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"sess-1"}},
			Metadata: map[string]any{},
		}
	}
	newSelector := func() *SessionAffinitySelector {
		// FillFirst prefers auth-a, so only a kept binding can choose auth-b.
		return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, TTL: time.Hour, StatePath: path})
	}

	first := newSelector()
	if got, err := first.Pick(context.Background(), "anthropic", "claude-sonnet", opts(), []*Auth{b}); err != nil || got.ID != "auth-b" {
		t.Fatalf("first pick = %v, %v", got, err)
	}
	first.Stop() // a restart; the state is flushed on stop

	second := newSelector()
	defer second.Stop()
	got, err := second.Pick(context.Background(), "anthropic", "claude-sonnet", opts(), []*Auth{a, b})
	if err != nil || got.ID != "auth-b" {
		t.Fatalf("after restart picked %v, %v; want auth-b", got, err)
	}

	// Failover is unchanged: a bound account that is gone does not hold the session.
	third := newSelector()
	defer third.Stop()
	got, err = third.Pick(context.Background(), "anthropic", "claude-sonnet", opts(), []*Auth{a})
	if err != nil || got.ID != "auth-a" {
		t.Fatalf("failover picked %v, %v; want auth-a", got, err)
	}
}

func TestPersistentSessionCacheDropsExpiredBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-affinity.state")
	first := NewPersistentSessionCache(50*time.Millisecond, path)
	first.Set("k", "auth-a")
	first.Stop()
	time.Sleep(80 * time.Millisecond)
	second := NewPersistentSessionCache(time.Hour, path)
	defer second.Stop()
	if id, ok := second.Get("k"); ok {
		t.Fatalf("expired binding came back as %q", id)
	}
}
