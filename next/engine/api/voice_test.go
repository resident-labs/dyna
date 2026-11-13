package api

import (
	"context"
	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type voiceService struct{ Service }

func (*voiceService) Voice(context.Context, contract.RoomID) (func(*websocket.Conn) error, *contract.Error) {
	return func(*websocket.Conn) error { return nil }, nil
}
func TestVoiceRouteAuthorizationAndOrigin(t *testing.T) {
	a := New(&voiceService{}, nil)
	for _, caller := range []Caller{boxCaller(), {Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}}} {
		w := httptest.NewRecorder()
		a.handler(caller).ServeHTTP(w, httptest.NewRequest("GET", "/v1/voice?room=r", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("unauthorized: %d", w.Code)
		}
	}
	server := httptest.NewServer(a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/voice?room=r"
	_, response, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://foreign.example"}}})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", response, err)
	}
	conn, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.CloseNow()
}
