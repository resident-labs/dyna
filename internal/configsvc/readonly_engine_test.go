package configsvc_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// A read-only engine refuses a live profile probe with websvc.ErrReadOnly,
// the one read-only error, so the engine answers 503 like every other call
// it cannot run now.
func TestReadOnlyLiveProbeGets503(t *testing.T) {
	s := &configsvc.Service{ReadOnly: true}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "ConfigCheckProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.ConfigCheckProfile}})
	if err != nil {
		t.Fatal(err)
	}
	path := enginetest.Serve(t, storetest.DataDir(t), calls)
	err = engineclient.New(path).Call(context.Background(), "ConfigCheckProfile", []any{"missing", true}, nil)
	var remote *engineclient.RemoteError
	if !errors.As(err, &remote) || remote.Status != http.StatusServiceUnavailable || remote.Message != "read-only mode" {
		t.Fatalf("live probe in read-only mode = %v; want 503 read-only mode", err)
	}
}
