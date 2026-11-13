package auth

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func resetPreferred(ctx context.Context, ids ...string) context.Context {
	encoded := make([]string, len(ids))
	for i, id := range ids {
		encoded[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
	}
	return WithDynaPreferredAuth(ctx, []string{strings.Join(encoded, ",")})
}

// The room's account outranks the order and a session's binding while it is usable, and
// the session comes back to it once it is usable again.
func TestPreferenceOutranksOrderAndAffinity(t *testing.T) {
	manager := resetManager(t, "first", "second")
	resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
	resetPublish(manager, "second", resetFutureWeek(50, 96*time.Hour))
	pick := func(ctx context.Context, tried map[string]struct{}) string {
		t.Helper()
		picked, _, err := manager.pickNext(ctx, "claude", "", resetSessionOptions("session-1"), tried)
		if err != nil || picked == nil {
			t.Fatalf("pick: %v, %v", picked, err)
		}
		return picked.ID
	}
	if got := pick(context.Background(), nil); got != "first" {
		t.Fatalf("bound = %s", got)
	}
	ctx := resetPreferred(context.Background(), "second")
	if got := pick(ctx, nil); got != "second" {
		t.Fatalf("preference lost to affinity: %s", got)
	}
	if got := pick(ctx, map[string]struct{}{"second": {}}); got != "first" {
		t.Fatalf("a refusal on the preferred account did not fall back: %s", got)
	}
	if got := pick(ctx, nil); got != "second" {
		t.Fatalf("the session did not come back: %s", got)
	}
	if got := pick(resetPinned("first"), nil); got != "first" {
		t.Fatalf("a pin lost to the preference: %s", got)
	}
}

// Unlike a pin, a preference keeps the gates, so a nearly spent account lets the room
// spill to the others instead of stalling.
func TestPreferenceFallsBackWhenNotUsable(t *testing.T) {
	for name, left := range map[string]float64{"spent": 0, "past a gate": 2} {
		t.Run(name, func(t *testing.T) {
			manager := resetManager(t, "first", "second")
			resetPublish(manager, "first", resetFutureWeek(50, 96*time.Hour))
			resetPublish(manager, "second", resetFutureWeek(left, 2*time.Hour))
			picked, _, err := manager.pickNext(resetPreferred(context.Background(), "second"), "claude", "", resetSessionOptions("session-"+name), nil)
			if err != nil || picked == nil || picked.ID != "first" {
				t.Fatalf("picked %v, %v", picked, err)
			}
		})
	}
}

// Several preferred accounts share the room in the usual order among themselves.
func TestSeveralPreferredKeepTheOrder(t *testing.T) {
	manager := resetManager(t, "first", "second", "third")
	resetPublish(manager, "first", resetFutureWeek(50, time.Hour))
	resetPublish(manager, "second", resetFutureWeek(50, 96*time.Hour))
	resetPublish(manager, "third", resetFutureWeek(50, 48*time.Hour))
	picked, _, err := manager.pickNext(resetPreferred(context.Background(), "second", "third"), "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || picked == nil || picked.ID != "third" {
		t.Fatalf("picked %v, %v", picked, err)
	}
}
