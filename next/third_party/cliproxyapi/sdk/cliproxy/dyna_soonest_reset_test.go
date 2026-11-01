package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestSoonestResetRoutingSelector(t *testing.T) {
	for _, value := range []string{"soonest-reset", "soonestreset", "soonest-reset-first", " Soonest-Reset "} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: value}})
		if state.strategy != "soonest-reset" {
			t.Fatalf("%q normalized to %q", value, state.strategy)
		}
		if _, ok := newRoutingSelector(state).(*coreauth.SoonestResetSelector); !ok {
			t.Fatalf("selector type = %T", newRoutingSelector(state))
		}
	}
}

func TestSoonestResetRoutingSelectorUnderSessionAffinity(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "soonest-reset", SessionAffinity: true}})
	if _, ok := newRoutingSelector(state).(*coreauth.SessionAffinitySelector); !ok {
		t.Fatalf("selector type = %T", newRoutingSelector(state))
	}
}

// A hot reload compares states and rebuilds the selector only when they differ.
func TestSoonestResetChangesRoutingStateOnReload(t *testing.T) {
	before := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}})
	after := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true, Strategy: "soonest-reset"}})
	if before == after {
		t.Fatal("a reload would keep the old selector")
	}
}
