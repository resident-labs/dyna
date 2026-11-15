package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type admissionFailoverSelector struct{ picks int }

func (s *admissionFailoverSelector) Pick(_ context.Context, _, model string, _ executor.Options, auths []*Auth) (*Auth, error) {
	s.picks++
	switch s.picks {
	case 1:
		return nil, newModelCooldownError(model, "", time.Millisecond)
	case 2:
		for _, a := range auths {
			if a.ID == "admission-a" {
				return a, nil
			}
		}
	}
	return nil, newModelCooldownError(model, "", time.Second)
}

type admissionFailoverExecutor struct{ dynaExecutor }

func (e *admissionFailoverExecutor) ExecuteStream(ctx context.Context, a *Auth, req executor.Request, opts executor.Options) (*executor.StreamResult, error) {
	_, err := e.Execute(ctx, a, req, opts)
	return nil, err
}

func TestDynaAdmissionDeadlineSurvivesFailover(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			selector := &admissionFailoverSelector{}
			manager.SetSelector(selector)
			exec := &admissionFailoverExecutor{dynaExecutor{successExecutor: successExecutor{provider: "admission-test"}, fail: "admission-a"}}
			manager.RegisterExecutor(exec)
			for _, id := range []string{"admission-a", "admission-b"} {
				if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "admission-test", Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "admission-test", []*registry.ModelInfo{{ID: "admission-model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			ctx := WithDynaEligibleAuth(context.Background(), []string{base64.RawURLEncoding.EncodeToString([]byte("admission-a")) + "," + base64.RawURLEncoding.EncodeToString([]byte("admission-b"))})
			// Shorten only this request's budget: the first cooldown fits, the cooldown
			// encountered after A fails does not. A reset deadline would wait and repick B.
			ctx = context.WithValue(ctx, dynaAdmissionDeadlineKey{}, time.Now().Add(500*time.Millisecond))
			start := time.Now()
			var err error
			if stream {
				_, err = manager.ExecuteStream(ctx, []string{"admission-test"}, executor.Request{Model: "admission-model"}, executor.Options{})
			} else {
				_, err = manager.Execute(ctx, []string{"admission-test"}, executor.Request{Model: "admission-model"}, executor.Options{})
			}
			if err == nil || selector.picks != 3 || len(exec.calls) != 1 || exec.calls[0] != "admission-a" {
				t.Fatalf("error=%v picks=%d calls=%v", err, selector.picks, exec.calls)
			}
			if time.Since(start) >= time.Second {
				t.Fatal("failover reset admission deadline")
			}
		})
	}
}
