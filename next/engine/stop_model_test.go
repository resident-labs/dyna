package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// Use real service Stop, its framed dispatch, PiG Abort and persisted output.
// The host channel is in-process; this does not reproduce a broken SSH link.
func TestSessionStopDuringSilentModel(t *testing.T) {
	for _, phase := range []string{"headers", "stream", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			s, _, worker, ctx := setup(t)
			caller := boxCaller(worker)
			channel, problem := testChannel(t, s, ctx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			frame := (<-channel).Body.(contract.TurnStart)
			received := make(chan struct{}, 10)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				received <- struct{}{}
				if phase == "backoff" {
					w.WriteHeader(503)
					return
				}
				if phase == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			dir := pigtest.AgentDir(t, pigtest.NewModel(), "")
			path := filepath.Join(dir, "models.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = regexp.MustCompile(`"baseUrl":"[^"]+"`).ReplaceAll(data, []byte(`"baseUrl":"`+server.URL+`"`))
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			h, err := pig.Open(context.Background(), pig.Config{Dir: t.TempDir(), AgentDir: dir, SessionDir: filepath.Join(dir, "sessions"), Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model"}, Instructions: "test"})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			done := make(chan contract.TurnEnded, 1)
			go func() {
				result, _ := h.Start(ctx, frame.Turn, []contract.TurnInput{{Delivery: 1, Text: "test"}}, func() {})
				done <- result
			}()
			select {
			case <-received:
			case <-time.After(5 * time.Second):
				t.Fatal("request not started")
			}
			if phase == "backoff" {
				time.Sleep(50 * time.Millisecond)
			}
			start := time.Now()
			answer, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker})
			apiElapsed := time.Since(start)
			if problem != nil || !answer.Stopping || apiElapsed > time.Second {
				t.Fatalf("Stop API %s: %+v %v", apiElapsed, answer, problem)
			}
			select {
			case message := <-channel:
				if _, ok := message.Body.(contract.TurnStop); !ok {
					t.Fatalf("frame %T", message.Body)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop not dispatched")
			}
			stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if err = h.Abort(stopCtx); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.Outcome != contract.TurnStopped {
					t.Fatalf("outcome %+v", result)
				}
				if _, problem = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: frame.Turn, Outcome: result.Outcome}); problem != nil {
					t.Fatal(problem)
				}
			case <-stopCtx.Done():
				t.Fatal("Stop not settled")
			}
			elapsed := time.Since(start)
			if elapsed > time.Second {
				t.Fatalf("Stop total %s", elapsed)
			}
			var retry int64
			if err = s.store.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id=?", worker).Scan(&retry); err != nil || retry != 0 {
				t.Fatalf("Stop scheduled retry %d: %v", retry, err)
			}
			t.Logf("Stop API %s, dispatch-to-persisted-end total %s", apiElapsed, elapsed)
		})
	}
}
