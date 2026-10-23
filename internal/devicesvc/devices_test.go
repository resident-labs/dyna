package devicesvc

import (
	"dyna-agent/internal/connect"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeviceViewsPairAndRevoke(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "sessions"))
	w, c, err := svc.deviceStores()
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreatePairing("https://dyna.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://dyna.example.com/#pair="+p.Code || time.Until(p.Expires) < 4*time.Minute {
		t.Fatalf("pair: %+v", p)
	}
	token, d, err := w.Pair(p.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.Pair(p.Code, "again"); err == nil {
		t.Fatal("code reused")
	}
	err = c.UpdateSessions(func(list []connect.Session) []connect.Session {
		return append(list, connect.Session{ID: "cs_test", Hash: "secret-hash", Old: []string{"old-secret"}, Name: "browser", End: time.Now().Add(time.Hour), Origin: "https://dyna.example.com"})
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.Devices()
	if err != nil || len(list) != 2 {
		t.Fatalf("devices: %+v %v", list, err)
	}
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), "secret") {
		t.Fatalf("device secret leaked: %s", data)
	}
	if n, err := svc.RevokeDevice("paired", d.ID); err != nil || n != 1 {
		t.Fatalf("revoke: %d %v", n, err)
	}
	if _, ok := w.Check(token); ok {
		t.Fatal("revoked cookie works")
	}
	if n, err := svc.RevokeDevice("all", "all"); err != nil || n != 1 {
		t.Fatalf("all revoke: %d %v", n, err)
	}
	if list, err = svc.Devices(); err != nil || len(list) != 0 {
		t.Fatalf("devices remain: %+v %v", list, err)
	}
	for _, origin := range []string{"javascript:alert(1)", "https://user:pass@host", "https://host/path", "https://host?code=x"} {
		if _, err = svc.CreatePairing(origin); err == nil {
			t.Fatalf("accepted origin %q", origin)
		}
	}
	if _, err = svc.CreatePairing(""); err != nil {
		t.Fatal(err)
	}
}

func TestExistingDeviceIsLegacyAndANewCodeIsNot(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	// Legacy grants come from retained files present before startup imports.
	now := time.Now()
	raw, err := json.Marshal([]devicestore.Device{{ID: "old-phone", Name: "old", Created: now, LastSeen: now, Expires: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "devices.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	svc := New(filepath.Join(root, "sessions"))
	w, _, err := svc.deviceStores()
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.Devices()
	if err != nil || len(list) != 1 || list[0].ScopeLabel != scope.LegacyFull || !list[0].Legacy {
		t.Fatalf("legacy device = %+v %v", list, err)
	}
	p, err := svc.CreatePairing("https://dyna.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, dev, err := w.Pair(p.Code, "new phone")
	// An unscoped code names no scopes: the route where pairing happens
	// picks the grant in the web server.
	if err != nil || len(dev.Scopes) != 0 {
		t.Fatalf("new code scopes = %#v %v", dev.Scopes, err)
	}
	dev.Scopes = scope.Default()
	grants, err := devicegrant.Open(filepath.Dir(svc.sessionRoot))
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Put("paired", dev.ID, dev.Scopes, false); err != nil {
		t.Fatal(err)
	}
	list, err = svc.Devices()
	if err != nil || len(list) != 2 {
		t.Fatalf("devices = %+v %v", list, err)
	}
	for _, d := range list {
		if d.ID == dev.ID && (d.Legacy || d.ScopeLabel != "read, operate") {
			t.Fatalf("new device = %+v", d)
		}
		if d.ID == "old-phone" && d.ScopeLabel != scope.LegacyFull {
			t.Fatalf("old device changed: %+v", d)
		}
	}
	if n, err := svc.RevokeDevice("paired", "old-phone"); err != nil || n != 1 {
		t.Fatalf("revoke = %d %v", n, err)
	}
	if _, ok, err := grants.Get("paired", "old-phone"); err != nil || ok {
		t.Fatalf("revoked grant remains: ok=%v err=%v", ok, err)
	}
}

func TestSetScopesReplacesALegacyGrant(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "sessions"))
	w, c, err := svc.deviceStores()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.IssueDevice("old-phone", "old", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Devices(); err != nil {
		t.Fatal(err)
	}
	got, err := svc.SetScopes("old-phone", []string{"files", "read"})
	if err != nil || got.Legacy || got.ScopeLabel != "read, files" {
		t.Fatalf("set = %+v %v", got, err)
	}
	grants, err := devicegrant.Open(filepath.Dir(svc.sessionRoot))
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := grants.Get("paired", "old-phone")
	if err != nil || !ok || row.Legacy || !reflect.DeepEqual(row.Scopes, []string{scope.Read, scope.Files}) {
		t.Fatalf("stored = %+v ok=%v err=%v", row, ok, err)
	}
	if err := c.UpdateSessions(func(list []connect.Session) []connect.Session {
		return append(list, connect.Session{ID: "cs_test", Name: "browser", End: time.Now().Add(time.Hour)})
	}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.SetScopes("cs_test", []string{"operate"})
	if err != nil || got.Kind != "connect" || got.ScopeLabel != "operate" {
		t.Fatalf("connect set = %+v %v", got, err)
	}
	if _, err = svc.SetScopes("missing", []string{"read"}); err == nil {
		t.Fatal("missing device was accepted")
	}
}

func TestDeviceCurrentMarkersDoNotChangeSharedList(t *testing.T) {
	original := DeviceList{{ID: "one", Kind: "paired"}, {ID: "two", Kind: "paired"}}
	first := original.ForPrincipal(websvc.Principal{Kind: "device", ID: "one"}).(DeviceList)
	second := first.ForPrincipal(websvc.Principal{Kind: "device", ID: "two"}).(DeviceList)
	if original[0].Current || original[1].Current || !first[0].Current || first[1].Current || second[0].Current || !second[1].Current {
		t.Fatalf("caller state leaked: original=%+v first=%+v second=%+v", original, first, second)
	}
}
