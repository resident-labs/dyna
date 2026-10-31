package auth

import (
	"context"
	"encoding/base64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"net/http"
	"strings"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDynaEligibility(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("allowed,opaque"))
	for _, tc := range []struct {
		name   string
		header http.Header
		id     string
		want   bool
	}{
		{"absent", nil, "other", true},
		{"allowed", http.Header{"X-Dyna-Eligible-Auth": {encoded}}, "allowed,opaque", true},
		{"denied", http.Header{"X-Dyna-Eligible-Auth": {encoded}}, "other", false},
		{"empty", http.Header{"X-Dyna-Eligible-Auth": {""}}, "other", false},
		{"invalid", http.Header{"X-Dyna-Eligible-Auth": {"!"}}, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if values, present := tc.header["X-Dyna-Eligible-Auth"]; present {
				ctx = WithDynaEligibleAuth(ctx, values)
			}
			got := authSelectionEligibilityForRequest(ctx, coreexecutor.Options{}).allows(&Auth{ID: tc.id})
			if got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

type dynaExecutor struct {
	successExecutor
	calls []string
	fail  string
}

func (e *dynaExecutor) Execute(ctx context.Context, a *Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls = append(e.calls, a.ID)
	if a.ID == e.fail {
		return coreexecutor.Response{}, &Error{HTTPStatus: 500, Message: "failed"}
	}
	return coreexecutor.Response{Payload: []byte(a.ID)}, nil
}

func TestDynaEligibilityExecution(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	defer selector.Stop()
	manager.SetSelector(selector)
	exec := &dynaExecutor{successExecutor: successExecutor{provider: "dyna-test"}}
	manager.RegisterExecutor(exec)
	for _, id := range []string{"dyna-a", "dyna-b", "dyna-c"} {
		_, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: id, Provider: "dyna-test", Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "dyna-test", []*registry.ModelInfo{{ID: "dyna-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	policy := func(ids ...string) context.Context {
		values := make([]string, len(ids))
		for i, id := range ids {
			values[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
		}
		return WithDynaEligibleAuth(ctx, []string{strings.Join(values, ",")})
	}
	req := coreexecutor.Request{Model: "dyna-model"}
	opts := coreexecutor.Options{Headers: http.Header{"X-Session-Id": {"session"}}}
	first, err := manager.Execute(policy("dyna-a"), []string{"dyna-test"}, req, opts)
	if err != nil || string(first.Payload) != "dyna-a" {
		t.Fatalf("first %s %v", first.Payload, err)
	}
	second, err := manager.Execute(policy("dyna-b"), []string{"dyna-test"}, req, opts)
	if err != nil || string(second.Payload) != "dyna-b" {
		t.Fatalf("affinity %s %v", second.Payload, err)
	}
	exec.calls = nil
	exec.fail = "dyna-b"
	third, err := manager.Execute(policy("dyna-b", "dyna-c"), []string{"dyna-test"}, req, opts)
	if err != nil || string(third.Payload) != "dyna-c" {
		t.Fatalf("retry %s %v calls %v", third.Payload, err, exec.calls)
	}
	for _, id := range exec.calls {
		if id == "dyna-a" {
			t.Fatal("retry escaped policy")
		}
	}
	if _, err = manager.Execute(policy(), []string{"dyna-test"}, req, opts); err == nil {
		t.Fatal("empty policy allowed request")
	}
}
