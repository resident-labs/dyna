package websvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCommandRoutesDispatchTypedArguments(t *testing.T) {
	e := newEnv(t)
	var got []json.RawMessage
	e.srv.o.Calls["RunCommand"] = func(_ context.Context, args []json.RawMessage) (any, error) {
		got = args
		return true, nil
	}
	e.srv.o.CallScopes["RunCommand"] = ScopeOperate
	req := httptest.NewRequest(http.MethodPost, "/api/rooms/alpha/commands/run", strings.NewReader(`{"name":"review","text":"check this","target":""}`))
	req.Header.Set("Origin", tsApp)
	req.Header.Set("X-Dyna", "1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.srv.commands(w, req, Route{App: tsApp}, Principal{Kind: "local"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var args []string
	for _, arg := range got {
		var value string
		if err := json.Unmarshal(arg, &value); err != nil {
			t.Fatal(err)
		}
		args = append(args, value)
	}
	if !reflect.DeepEqual(args, []string{"alpha", "review", "check this", ""}) {
		t.Fatalf("arguments: %v", args)
	}
}
