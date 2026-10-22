package engine

import (
	"context"
	"net"
	"net/http"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

// localCallerContext uses the production socket context, without a listener.
func localCallerContext(t *testing.T, caller websvc.Caller, actor authority.Actor) context.Context {
	t.Helper()
	devices, err := devicestore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{
		Store: devices, Routes: websvc.LoopbackRoutes("7457", "7458"),
		Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}},
		Hub:   hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler(),
		Callers: func(net.Conn) (websvc.Caller, error) { return caller, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority.WithActor(srv.UnixConnContext(t.Context(), nil), actor)
}
