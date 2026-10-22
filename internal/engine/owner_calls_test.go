package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing/fstest"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc/devicestore"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// Wrong-arity arguments stop at the real binder, so allowed calls prove they
// pass the owner guard without running any owner action.
func TestOwnerCallsUseCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls, _, err := callsFor(testServices(t, false), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := authority.Actor{Role: authority.Owner}
	cases := []struct {
		name  string
		ctx   context.Context
		allow bool
	}{
		{"session", localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "session"}, authority.Actor{Role: authority.Session, Session: "session", Room: "room"}), false},
		{"root", localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "root"}, authority.Actor{Role: authority.Root, Session: "root", Room: "room"}), false},
		{"desktop owner", localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, owner), true},
		{"paired owner", authority.WithActor(t.Context(), owner), true},
	}
	args := make([]json.RawMessage, 64)
	for i := range args {
		args[i] = json.RawMessage("null")
	}
	for _, name := range ownerCalls {
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					_, err := calls[name](tc.ctx, args)
					if err == nil {
						t.Fatal("wrong-arity call ran an owner action")
					}
					if tc.allow {
						if !strings.Contains(err.Error(), "takes ") || !strings.Contains(err.Error(), "got 64") {
							t.Fatalf("owner was refused before argument validation: %v", err)
						}
					} else if !strings.Contains(err.Error(), "is owner only") {
						t.Fatalf("caller reached owner action: %v", err)
					}
				})
			}
		})
	}
}

// A phone using a paired remote route acts as the owner through the same
// caller middleware as the desktop. Existing device scopes still apply.
func TestPairedRemoteDevicePassesEveryOwnerCall(t *testing.T) {
	storetest.Isolate(t)
	calls, scopes, err := callsFor(testServices(t, false), false)
	if err != nil {
		t.Fatal(err)
	}
	(&socketCallers{}).actForCallers(calls)
	devices, err := devicestore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code, err := devices.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, device, err := devices.Pair(code, "Owner phone")
	if err != nil {
		t.Fatal(err)
	}
	grants, err := devicegrant.Open(filepath.Dir(devices.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Put("paired", device.ID, scope.All(), false); err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{
		Store: devices, Routes: []websvc.Route{{App: "https://owner.example", Cards: "https://cards.owner.example"}},
		Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}},
		Calls: calls, CallScopes: scopes, Hub: hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler(),
	})
	if err != nil {
		t.Fatal(err)
	}
	args := make([]any, 64)
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ownerCalls {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://owner.example/api/"+name, strings.NewReader(string(body)))
			req.RemoteAddr = "203.0.113.20:4321"
			req.Header.Set("Origin", "https://owner.example")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Dyna", "1")
			req.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: token})
			response := httptest.NewRecorder()
			srv.AppHandler().ServeHTTP(response, req)
			if !strings.Contains(response.Body.String(), "got 64") {
				t.Fatalf("paired owner was refused before argument validation: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
