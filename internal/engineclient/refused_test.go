package engineclient

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// A call the engine refuses fails with the engine's own line, so the CLI
// exits non-zero and names the cause (item 26).
func TestRefusedCallKeepsTheEngineLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	const line = "the engine refused this call: rule (b): the caller runs in dyna-x.scope, under dyna.slice, which Dyna did not record for a session"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"` + line + `"}`))
	})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	err = noWait(path).Call(context.Background(), "Health", nil, nil)
	if err == nil || err.Error() != line {
		t.Fatalf("Call = %v, want the engine's refusal line", err)
	}
}
