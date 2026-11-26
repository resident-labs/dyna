package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Dyna: reproduce the account topology from the November 13 quota incident.
func TestDynaClaudeAffinityQuotaFailover(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model = "claude-fable-5-2"
	const primary = "dyna-oauth-primary"
	const spare = "claude-legacy-spare.json"
	ctx := WithDynaEligibleAuth(context.Background(), []string{base64.RawURLEncoding.EncodeToString([]byte(primary)), base64.RawURLEncoding.EncodeToString([]byte(spare))})
	selector := NewSessionAffinitySelector(&SoonestResetSelector{})
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})
	for _, id := range []string{primary, spare} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := manager.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	manager.PublishQuotaReading(primary, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Seconds: 604800, UsedPercent: 40, ResetAt: now.Add(24 * time.Hour)}, {Seconds: 18000, UsedPercent: 20, ResetAt: now.Add(75 * time.Minute)}}})
	// A fresh reading, not a previous refusal, keeps the spare off this model.
	deadline := now.Add(5 * 24 * time.Hour)
	manager.PublishQuotaReading(spare, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Model: "fable", Seconds: 604800, UsedPercent: 100, ResetAt: deadline}}})
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"bound-session"}}}
	picked, err := manager.SelectAuth(ctx, "claude", model, opts)
	if err != nil || picked.ID != primary {
		t.Fatalf("initial pick = %v, %v", picked, err)
	}
	now = time.Now()
	manager.PublishQuotaReading(primary, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Model: "fable", Seconds: 604800, UsedPercent: 100, ResetAt: deadline}}})
	manager.PublishQuotaReading(spare, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Seconds: 604800, UsedPercent: 75, ResetAt: deadline}, {Model: "fable", Seconds: 604800, UsedPercent: 0, ResetAt: deadline}}})
	picked, err = manager.SelectAuth(ctx, "claude", model, opts)
	if err != nil || picked.ID != spare {
		t.Fatalf("failover pick = %v, %v", picked, err)
	}
}
