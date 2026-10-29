package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"dyna-agent/internal/callspec"
)

// connContextFunc is the socket server's current caller-identity hook. Before
// the full server exists, Health needs no identity beyond the private socket.
type connContextFunc func(context.Context, net.Conn) context.Context

// startupHealthHandler serves only Health while migrations run. Every other
// call waits for the full handler, so no client can write an unmigrated store.
func startupHealthHandler(started time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/Health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Health{Status: healthStarting, Started: started})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "the engine is starting", "code": callspec.CodeEngineStarting})
	})
	return mux
}
