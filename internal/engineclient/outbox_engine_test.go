package engineclient_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// The engine's own handler answers a method's write deadline with 503, not
// 422, so the outbox sends the write again with the same request ID instead
// of dropping it as refused.
func TestOutboxResendsAWriteTheEngineCouldNotFinish(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	calls, err := websvc.Bind([]websvc.Binding{{Name: "Flaky", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true,
		Method: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, callspec.RequestOf(ctx))
			if len(ids) == 1 {
				return fmt.Errorf("write the session: %w", context.DeadlineExceeded)
			}
			return nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	path := enginetest.Serve(t, storetest.DataDir(t), calls)
	o := engineclient.NewOutbox(engineclient.New(path), "run")
	if err := o.Call(t.Context(), "Flaky"); err != nil {
		t.Fatalf("Call after a deadline error = %v; want the resend to land", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	o.Close(ctx)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(ids) != fmt.Sprint([]string{"run/1", "run/1"}) {
		t.Fatalf("engine saw request IDs %v; want run/1 twice", ids)
	}
}
