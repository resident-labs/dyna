package websvc

import (
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
)

func TestComputerBrowserGrantIsRecordedByTheEngine(t *testing.T) {
	e := newEnv(t)
	token, dev, err := e.store.IssueDevice("browser-local01", "dyna browser · local", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: token}); w.Code != http.StatusOK {
		t.Fatalf("browser echo = %d %s", w.Code, w.Body)
	}
	grants, err := devicegrant.Open(filepath.Dir(e.store.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := grants.Get("paired", dev.ID)
	if err != nil || !ok || got.Legacy || !reflect.DeepEqual(got.Scopes, scope.All()) {
		t.Fatalf("grant = %+v ok=%v err=%v", got, ok, err)
	}
	if err := grants.Put("paired", dev.ID, []string{scope.Read}, false); err != nil {
		t.Fatal(err)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: token}); w.Code != http.StatusOK {
		t.Fatalf("narrowed echo = %d %s", w.Code, w.Body)
	}
	got, ok, err = grants.Get("paired", dev.ID)
	if err != nil || !ok || !reflect.DeepEqual(got.Scopes, []string{scope.Read}) {
		t.Fatalf("narrowed grant = %+v ok=%v err=%v", got, ok, err)
	}
	phoneToken, phone, err := e.store.IssueDevice("", "phone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: phoneToken}); w.Code != http.StatusForbidden {
		t.Fatalf("phone = %d %s", w.Code, w.Body)
	}
	if _, ok, err := grants.Get("paired", phone.ID); err != nil || ok {
		t.Fatalf("phone grant ok=%v err=%v", ok, err)
	}
}
