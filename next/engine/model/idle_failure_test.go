package model

import (
	"context"
	"io"
	"net"
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

func TestIdleStreamReachesExistingModelFailure(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "stream"}[headers], func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "s")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if headers {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})}
			go upstream.Serve(listener)
			defer upstream.Close()
			route := httptest.NewServer(handler(&http.Client{Transport: idleTransport(socket, 40*time.Millisecond)}, "key", "session", []string{"test-model"}, []string{"account"}, "", false))
			defer route.Close()
			dir := pigtest.AgentDir(t, pigtest.NewModel(), "")
			path := filepath.Join(dir, "models.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = regexp.MustCompile(`"baseUrl":"[^"]+"`).ReplaceAll(data, []byte(`"baseUrl":"`+route.URL+`/model`+`"`))
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"defaultProjectTrust":"always","retry":{"enabled":false}}`), 0600); err != nil {
				t.Fatal(err)
			}
			h, err := pig.Open(context.Background(), pig.Config{Dir: t.TempDir(), AgentDir: dir, SessionDir: filepath.Join(dir, "sessions"), Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model"}, Instructions: "test"})
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := h.Start(ctx, "idle-turn", []contract.TurnInput{{Delivery: 1, Text: "hello"}}, func() {})
			if err != nil || result.Outcome != contract.TurnFailed || result.Cause != contract.CauseModelFailed || result.Error == "" {
				t.Fatalf("timeout did not surface as existing model failure: %+v %v", result, err)
			}
		})
	}
}
