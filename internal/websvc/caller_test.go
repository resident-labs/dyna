package websvc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// asCaller serves h as a Unix socket connection that UnixConnContext
// identified as id, for tests that call the handler directly.
func asCaller(h http.Handler, id identified) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerContextKey{}, id)))
	})
}

var owner = identified{caller: Caller{Kind: Owner}}

// A refused or unidentified caller on the Unix socket is stopped before any
// call runs, with one line that names the cause (item 26).
func TestUnixRefusesCallersBeforeAnyCall(t *testing.T) {
	e := newEnv(t)
	q := req{url: "http://engine/api/Echo", body: `["socket",1]`, origin: "-", noDyna: true}
	refused := identified{refused: errors.New("rule (b): the caller runs in dyna-made-up.scope, under dyna.slice, which Dyna did not record for a session")}
	for name, h := range map[string]http.Handler{
		"unidentified": e.srv.UnixHandler(),
		"refused":      asCaller(e.srv.UnixHandler(), refused),
	} {
		w := e.do(h, q)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "the engine refused this call") {
			t.Fatalf("%s caller = %d %s", name, w.Code, w.Body)
		}
		if name == "refused" && !strings.Contains(w.Body.String(), "rule (b)") {
			t.Fatalf("the refusal does not name its rule: %s", w.Body)
		}
	}
	session := identified{caller: Caller{Kind: Session, Session: "ses_a"}}
	if w := e.do(asCaller(e.srv.UnixHandler(), session), q); w.Code != http.StatusOK {
		t.Fatalf("an identified session = %d %s", w.Code, w.Body)
	}
}

// UnixConnContext identifies a real connection once, with the Callers
// option, and requests on it carry that identity.
func TestUnixConnContextIdentifiesTheConnection(t *testing.T) {
	e := newEnv(t)
	var seen atomic.Int32
	e.srv.o.Callers = func(c net.Conn) (Caller, error) {
		seen.Add(1)
		return Caller{}, errors.New("rule (c): test")
	}
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: e.srv.UnixHandler(), ConnContext: e.srv.UnixConnContext}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	res, err := client.Post("http://engine/api/Echo", "application/json", strings.NewReader(`["x",1]`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "rule (c): test") || seen.Load() != 1 {
		t.Fatalf("socket call = %d %s after %d identifications", res.StatusCode, body, seen.Load())
	}
}

// The web server knows a caller only as a Caller. It must not pull in the
// client or the install code (#1422, item 3), not even through callerid,
// which subagent uses to start its runners.
func TestWebsvcDependsOnNoClientCode(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"dyna-agent/internal/engineclient", "dyna-agent/internal/userservices"} {
		for _, dep := range strings.Fields(string(out)) {
			if dep == pkg {
				t.Errorf("websvc depends on %s", pkg)
			}
		}
	}
}

// Storage readers must not bring the workflow JavaScript parser into every
// websvc client, including the desktop module.
func TestRunstoreDependsOnNoWorkflowParser(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "dyna-agent/internal/runstore").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/dop251/goja") {
			t.Errorf("runstore depends on workflow parser package %s", dep)
		}
	}
}
