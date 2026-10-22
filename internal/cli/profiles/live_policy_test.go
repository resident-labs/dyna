package profiles

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestLiveProfileCheckUsesEngineCallerPower(t *testing.T) {
	storetest.Isolate(t)
	var called atomic.Int32
	svc := &configsvc.Service{}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "ConfigCheckProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.ConfigCheckProfile}})
	if err != nil {
		t.Fatal(err)
	}
	call := calls["ConfigCheckProfile"]
	calls["ConfigCheckProfile"] = func(ctx context.Context, args []json.RawMessage) (any, error) {
		called.Add(1)
		return call(authority.WithActor(ctx, authority.Actor{Role: authority.Session, Session: "session"}), args)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	// Cleared stamps and an owner actor in the CLI cannot replace the engine caller.
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	err = runProfileProbe(ctx, profile.Profile{Name: "missing"})
	if called.Load() != 1 || err == nil || !strings.Contains(err.Error(), "ConfigCheckProfile is owner or root only") {
		t.Fatalf("engine caller rule was bypassed: calls=%d err=%v", called.Load(), err)
	}
}
