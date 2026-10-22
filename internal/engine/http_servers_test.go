package engine

import (
	"context"
	"net/http"
	"testing"

	"dyna-agent/internal/websvc"
)

func TestHTTPServersNeverUseDefaultServeMux(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	servers := httpServers(ctx, &websvc.Server{})
	cancel()
	if len(servers) != 3 {
		t.Fatalf("got %d listeners, want app, card and Unix", len(servers))
	}
	for i, server := range servers {
		if server.BaseContext(nil).Err() != context.Canceled {
			t.Fatalf("listener %d does not cancel requests with the engine", i)
		}
		if server.Handler == nil || server.Handler == http.DefaultServeMux {
			t.Fatalf("listener %d exposes DefaultServeMux, including private profiling handlers", i)
		}
	}
}
