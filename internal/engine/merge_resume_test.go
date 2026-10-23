package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

func TestBrowserResumeMergingPreservesOtherMergeControls(t *testing.T) {
	s := testServices(t, false)
	t.Cleanup(authority.InEngine())
	rm, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(s.rooms.Notify.Rooms, rm.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	pause := func() {
		t.Helper()
		if err := s.rooms.PRs.MergeControl(owner, rm.ID, "pause", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	pause()
	calls, scopes, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	devices, err := devicestore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code, err := devices.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, device, err := devices.Pair(code, "Resume browser")
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
	server, err := websvc.New(websvc.Options{Store: devices, Routes: websvc.LoopbackRoutes("7457", "7458"), Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}, Calls: calls, CallScopes: scopes, Hub: hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7457/api/"+method, strings.NewReader(body))
		req.Header.Set("Origin", "http://127.0.0.1:7457")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dyna", "1")
		req.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: token})
		res := httptest.NewRecorder()
		server.AppHandler().ServeHTTP(res, req)
		return res
	}
	body := `["` + rm.ID + `"]`
	if res := request("ResumeMerging", body); res.Code != http.StatusOK {
		t.Fatalf("browser resume: %d %s", res.Code, res.Body)
	}
	status, err := s.rooms.PRs.MergeControlStatus(rm.ID)
	if err != nil || status.Paused {
		t.Fatalf("resume left pause: %+v %v", status, err)
	}
	pause()
	// The general merge control must remain private to the engine socket.
	if res := request("MergeControl", `["`+rm.ID+`","resume",[],[]]`); res.Code == http.StatusOK {
		t.Fatal("browser reached general merge controls")
	}
	if err := grants.Put("paired", device.ID, []string{scope.Read}, false); err != nil {
		t.Fatal(err)
	}
	if res := request("ResumeMerging", body); res.Code != http.StatusForbidden {
		t.Fatalf("read-only browser resume: %d %s", res.Code, res.Body)
	}
	status, err = s.rooms.PRs.MergeControlStatus(rm.ID)
	if err != nil || !status.Paused {
		t.Fatalf("denied request changed pause: %+v %v", status, err)
	}
}
