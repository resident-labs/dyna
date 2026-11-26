package auth

import (
	"context"
	"encoding/base64"
	"testing"
	"time"
)

func resetPinned(id string) context.Context {
	return WithDynaPinnedAuth(context.Background(), []string{base64.RawURLEncoding.EncodeToString([]byte(id))})
}

func TestPinOutranksAffinityAndRanking(t *testing.T) {
	manager := resetManager(t, "first", "second")
	resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
	resetPublish(manager, "second", resetFutureWeek(2, 96*time.Hour))
	bound, _, err := manager.pickNext(context.Background(), "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || bound.ID != "first" {
		t.Fatalf("bound = %v, %v", bound, err)
	}
	pinned, _, err := manager.pickNext(resetPinned("second"), "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || pinned.ID != "second" {
		t.Fatalf("pin lost to affinity: %v, %v", pinned, err)
	}
}

func TestPinFallsBackToAutomatic(t *testing.T) {
	tests := []struct {
		name       string
		pin        string
		tried      map[string]struct{}
		secondLeft float64
	}{
		{"spent", "second", nil, 0},
		{"removed", "gone", nil, 50},
		{"refused this request", "first", map[string]struct{}{"first": {}}, 50},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := resetManager(t, "first", "second")
			resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
			resetPublish(manager, "second", resetFutureWeek(test.secondLeft, 96*time.Hour))
			picked, _, err := manager.pickNext(resetPinned(test.pin), "claude", "", resetSessionOptions("session-"+test.name), test.tried)
			if err != nil {
				t.Fatalf("a pin failed the turn: %v", err)
			}
			if picked == nil || picked.ID == test.pin {
				t.Fatalf("picked %v", picked)
			}
		})
	}
}

func TestMalformedPinIsIgnored(t *testing.T) {
	ctx := WithDynaPinnedAuth(context.Background(), []string{"not base64!"})
	if id, _ := ctx.Value(dynaPinnedContextKey{}).(string); id != "" {
		t.Fatalf("pin = %q", id)
	}
}
