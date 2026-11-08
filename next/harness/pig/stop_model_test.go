package pig

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
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

func TestStopHungModelReturnsPromptly(t *testing.T) {
	for _, phase := range []string{"headers", "stream", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			received := make(chan struct{}, 1)
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
			c := config(t, pigtest.NewModel(), "")
			path := filepath.Join(c.AgentDir, "models.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = regexp.MustCompile(`"baseUrl":"[^"]+"`).ReplaceAll(data, []byte(`"baseUrl":"`+server.URL+`"`))
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			h, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			done := make(chan contract.TurnEnded, 1)
			go func() {
				result, _ := h.Start(context.Background(), "turn-stop", []contract.TurnInput{{Delivery: 1, Text: "hello"}}, func() {})
				done <- result
			}()
			select {
			case <-received:
			case <-time.After(5 * time.Second):
				t.Fatal("no model request")
			}
			// Give the 503 time to enter the existing agent retry sleep.
			if phase == "backoff" {
				time.Sleep(50 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			start := time.Now()
			err = h.Abort(ctx)
			elapsed := time.Since(start)
			if err != nil || elapsed > time.Second {
				t.Fatalf("Stop elapsed=%s err=%v", elapsed, err)
			}
			select {
			case result := <-done:
				if result.Outcome != contract.TurnStopped || result.Cause != "" {
					t.Fatalf("Stop became model failure: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal("turn did not settle")
			}
			t.Logf("Stop %s: %s", phase, elapsed)
		})
	}
}
