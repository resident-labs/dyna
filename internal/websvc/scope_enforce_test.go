package websvc

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"dyna-agent/internal/scope"
)

func TestScopeNamesMatchGrantPackage(t *testing.T) {
	if string(ScopeRead) != scope.Read || string(ScopeOperate) != scope.Operate || string(ScopeFiles) != scope.Files || string(ScopeTerminal) != scope.Terminal || string(ScopeComputers) != scope.Computers || string(ScopeAdmin) != scope.Admin {
		t.Fatal("websvc scopes and internal/scope names differ")
	}
	for _, name := range scope.All() {
		if !ValidScope(Scope(name)) {
			t.Fatalf("grant scope %q is not a websvc scope", name)
		}
	}
}

func TestDefaultConnectIsRefusedOutsideReadAndOperate(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Ext = terminalExtension{result: AuthOK}
	e.putGrant("connect", "cs_test", scope.Default())
	e.srv.o.Calls, e.srv.o.CallScopes = scopeProbeCalls()
	allowed := map[string]bool{"Send": true, "Rooms": true}
	for _, name := range []string{"Send", "Rooms", "TermOpen", "ComputerControl", "SetSetting", "ListEntries", "CreatePairing", "SetDeviceScopes"} {
		w := e.do(e.app, req{url: tsApp + "/api/" + name, body: `[]`})
		want := http.StatusForbidden
		if allowed[name] {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Errorf("%s = %d, want %d (%s)", name, w.Code, want, w.Body)
		}
	}
}

func TestReadOnlyDeviceCannotSend(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	dev, ok := e.store.Check(tok)
	if !ok {
		t.Fatal("paired device missing")
	}
	e.putGrant("paired", dev.ID, []string{scope.Read})
	e.srv.o.Calls, e.srv.o.CallScopes = scopeProbeCalls()
	if w := e.do(e.app, req{url: tsApp + "/api/Send", body: `[]`, cookie: tok}); w.Code != http.StatusForbidden {
		t.Fatalf("Send = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Rooms", body: `[]`, cookie: tok}); w.Code != http.StatusOK {
		t.Fatalf("Rooms = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/CardURL", body: `["room","att"]`, cookie: tok}); w.Code != http.StatusBadRequest {
		t.Fatalf("CardURL = %d %s", w.Code, w.Body)
	}
	e.putGrant("paired", dev.ID, scope.Default())
	if w := e.do(e.app, req{url: tsApp + "/api/TermOpen", body: `[]`, cookie: tok}); w.Code != http.StatusForbidden {
		t.Fatalf("TermOpen = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Send", body: `[]`, cookie: tok}); w.Code != http.StatusOK {
		t.Fatalf("Send with operate = %d %s", w.Code, w.Body)
	}
}

func TestUntaggedMethodIsClosedForAScopedDevice(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	e.srv.o.Calls["Untagged"] = func(context.Context, []json.RawMessage) (any, error) { return "ran", nil }
	if w := e.do(e.app, req{url: tsApp + "/api/Untagged", body: `[]`, cookie: tok}); w.Code != http.StatusForbidden {
		t.Fatalf("device = %d %s", w.Code, w.Body)
	}
	if w := e.do(asCaller(e.srv.UnixHandler(), owner), req{url: "http://engine/api/Untagged", body: `[]`, origin: "-", noDyna: true}); w.Code != http.StatusOK {
		t.Fatalf("local = %d %s", w.Code, w.Body)
	}
	if w := e.do(asCaller(e.srv.UnixHandler(), owner), req{url: "http://engine/api/Missing", body: `[]`, origin: "-", noDyna: true}); w.Code != http.StatusNotFound {
		t.Fatalf("local unknown = %d %s", w.Code, w.Body)
	}
}

func TestTerminalAndComputerAndVoiceNeedTheirScopes(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Ext = terminalExtension{result: AuthOK}
	e.putGrant("connect", "cs_test", scope.Default())
	if w := e.do(e.app, req{url: tsApp + "/api/terminal/attach", body: `{"id":"term_missing"}`}); w.Code != http.StatusForbidden {
		t.Fatalf("terminal default = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/computers/ses_a/shot.png"}); w.Code != http.StatusForbidden {
		t.Fatalf("computer default = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/voice/realtime"}); w.Code != http.StatusNotFound {
		t.Fatalf("voice with operate = %d %s", w.Code, w.Body)
	}
	e.putGrant("connect", "cs_test", []string{scope.Read})
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/voice/realtime"}); w.Code != http.StatusForbidden {
		t.Fatalf("voice without operate = %d %s", w.Code, w.Body)
	}
	e.putGrant("connect", "cs_test", []string{scope.Terminal})
	if w := e.do(e.app, req{url: tsApp + "/api/terminal/attach", body: `{"id":"term_missing"}`}); w.Code != http.StatusNotFound {
		t.Fatalf("terminal granted = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/events"}); w.Code != http.StatusForbidden {
		t.Fatalf("events without read = %d %s", w.Code, w.Body)
	}
	e.putGrant("connect", "cs_test", scope.All())
	if w := e.do(e.app, req{url: tsApp + "/api/terminal/attach", body: `{"id":"term_missing"}`}); w.Code != http.StatusNotFound {
		t.Fatalf("full grant terminal = %d %s", w.Code, w.Body)
	}
	if w := e.do(asCaller(e.srv.UnixHandler(), owner), req{url: "http://engine/api/terminal/attach", body: `{"id":"term_missing"}`, origin: "-", noDyna: true}); w.Code != http.StatusNotFound {
		t.Fatalf("local terminal = %d %s", w.Code, w.Body)
	}
}

func TestMissingGrantAllowsNothing(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Ext = terminalExtension{result: AuthOK}
	e.srv.o.Calls, e.srv.o.CallScopes = scopeProbeCalls()
	if w := e.do(e.app, req{url: tsApp + "/api/Rooms", body: `[]`}); w.Code != http.StatusForbidden {
		t.Fatalf("missing grant = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/attachments/room/abc"}); w.Code != http.StatusForbidden {
		t.Fatalf("attachment = %d %s", w.Code, w.Body)
	}
}

func scopeProbeCalls() (map[string]Call, map[string]Scope) {
	names := []struct {
		name  string
		scope Scope
	}{
		{"Send", ScopeOperate},
		{"Rooms", ScopeRead},
		{"TermOpen", ScopeTerminal},
		{"ComputerControl", ScopeComputers},
		{"SetSetting", ScopeAdmin},
		{"ListEntries", ScopeFiles},
		{"CreatePairing", ScopeAdmin},
		{"SetDeviceScopes", ScopeAdmin},
	}
	calls := map[string]Call{}
	scopes := map[string]Scope{}
	for _, n := range names {
		calls[n.name] = func(context.Context, []json.RawMessage) (any, error) { return "ok", nil }
		scopes[n.name] = n.scope
	}
	return calls, scopes
}
