package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

func TestWorktreeCreatorComesFromRecordedCaller(t *testing.T) {
	owner := authority.Actor{Role: authority.Owner}
	for _, tc := range []struct {
		name    string
		caller  websvc.Caller
		actor   authority.Actor
		enforce bool
		want    string
		logged  bool
	}{
		{"owner", websvc.Caller{Kind: websvc.Owner}, owner, false, "", false},
		{"session", websvc.Caller{Kind: websvc.Session, Session: "session"}, authority.Actor{Role: authority.Session, Session: "session"}, false, "session", false},
		{"worker has no run attribution", websvc.Caller{Kind: websvc.Worker, Run: "run", Agent: 2}, authority.Actor{Role: authority.Worker, Run: "run", Agent: 2}, false, "", false},
		{"report-only unverified session", websvc.Caller{Kind: websvc.Unverified}, owner, false, "unverified", true},
		{"report-only unresolved session", websvc.Caller{Kind: websvc.Session, Session: "session"}, owner, false, "unverified", true},
		{"enforced unverified caller", websvc.Caller{Kind: websvc.Unverified}, authority.Actor{}, true, "error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs []string
			got, err := worktreeCreatedBy(tc.caller, true, tc.actor, tc.enforce, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
			if err != nil {
				got = "error"
			}
			if got != tc.want || (len(logs) > 0) != tc.logged {
				t.Fatalf("creator = %q, error = %v, logs = %q", got, err, logs)
			}
		})
	}
}

func TestPairedDeviceCannotUseWorktreeCommands(t *testing.T) {
	services := testServices(t, false)
	calls, scopes, err := callsFor(services, false)
	if err != nil {
		t.Fatal(err)
	}
	callers := &socketCallers{}
	callers.actForCallers(calls)
	st, err := devicestore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, device, err := st.Pair(code, "Worktree device")
	if err != nil {
		t.Fatal(err)
	}
	grants, err := devicegrant.Open(filepath.Dir(st.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Put("paired", device.ID, scope.All(), false); err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{
		Store: st, Routes: websvc.LoopbackRoutes("7457", "7458"),
		Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}},
		Calls: calls, CallScopes: scopes, Hub: hub.New(1),
		Files: http.NotFoundHandler(), Cards: http.NotFoundHandler(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, args string }{
		{"NewWorktree", "[\"/missing\",\"feature\",\"origin/main\",true]"},
		{"GCWorktrees", "[\"/missing\",\"\",false]"},
		{"RemoveWorktrees", "[[\"missing\"],true]"},
		{"PruneWorktrees", "[0,true,false]"},
		{"ApplyRunWorktreeDisposition", "[\"missing\",\"remove-all\"]"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7457/api/"+tc.method, strings.NewReader(tc.args))
			r.Header.Set("Origin", "http://127.0.0.1:7457")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Dyna", "1")
			r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: token})
			w := httptest.NewRecorder()
			srv.AppHandler().ServeHTTP(w, r)
			if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "available only through the local engine socket") {
				t.Fatalf("response = %d %s", w.Code, w.Body)
			}
		})
	}
}
