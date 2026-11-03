package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestDynaInsufficientBalanceBlocksCredential(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, &SoonestResetSelector{}, nil)
	ctx := context.Background()
	for _, id := range []string{"empty", "funded"} {
		if _, err := m.Register(ctx, &Auth{ID: id, Provider: "openai-compatible-deepseek", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	m.MarkResult(ctx, Result{AuthID: "empty", Provider: "openai-compatible-deepseek", Model: "deepseek-chat", Error: &Error{HTTPStatus: http.StatusPaymentRequired, Message: `{"error":{"message":"Insufficient Balance"}}`}})
	empty, _ := m.GetByID("empty")
	if blocked, _, _ := isAuthBlockedForModel(empty, "deepseek-reasoner", time.Now()); !blocked {
		t.Fatal("empty account can still serve sibling model")
	}
	if empty.LastError == nil || empty.LastError.Code != "insufficient_balance" {
		t.Fatalf("missing typed reason: %+v", empty.LastError)
	}
	snapshot := m.QuotaRoutingSnapshot()
	for _, view := range snapshot.Providers["openai-compatible-deepseek"] {
		if view.AuthIndex == empty.Index && (view.State != RoutingStateCooldown || view.Reason != ErrorCodeInsufficientBalance) {
			t.Fatalf("empty account lacks cooldown or typed reason: %+v", view)
		}
	}
}
