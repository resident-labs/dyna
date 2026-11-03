package auth

import (
	"context"
	"os"
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
	past := time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	state := `[{"auth_id":"auth-old","expires_at":"` + past + `","aliases":["old"]},{"auth_id":"auth-new","expires_at":"` + future + `","aliases":["new"]}]`
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewPersistentSessionCache(time.Hour, path)
	defer c.Stop()
	if id, ok := c.Get("old"); ok {
		t.Fatalf("expired binding came back as %q", id)
	}
	if id, ok := c.Get("new"); !ok || id != "auth-new" {
		t.Fatalf("live binding = %q, %v", id, ok)
	}
}

func TestLoadedBindingFailsOverWhenItsAuthIsInvalidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-affinity.state")
	c := NewPersistentSessionCache(time.Hour, path)
	c.Set("k", "auth-a")
	c.Stop()
	loaded := NewPersistentSessionCache(time.Hour, path)
	defer loaded.Stop()
	if id, ok := loaded.Get("k"); !ok || id != "auth-a" {
		t.Fatalf("loaded binding = %q, %v", id, ok)
	}
	loaded.InvalidateAuth("auth-a")
	if id, ok := loaded.Get("k"); ok {
		t.Fatalf("invalidated binding still bound to %q", id)
	}
}
