package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/contract"
)

type terminalService struct {
	Service
	argv    []string
	problem *contract.Error
	done    chan struct{}
}

func (s *terminalService) Terminal(context.Context, contract.SessionID, contract.HostName) ([]string, func(), *contract.Error) {
	return s.argv, func() {
		if s.done != nil {
			close(s.done)
		}
	}, s.problem
}

func TestTerminalRoute(t *testing.T) {
	s := &terminalService{argv: []string{"sh", "-c", `echo "term=$TERM"; stty size; read line; stty size; echo "$line"; exit 3`}}
	a := New(s, nil)
	w := httptest.NewRecorder()
	a.handler(boxCaller()).ServeHTTP(w, httptest.NewRequest("GET", "/v1/terminal?box=s&cols=80&rows=24", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("session status %d", w.Code)
	}
	s.problem = &contract.Error{Code: contract.ErrConflict, Message: "not ready"}
	w = httptest.NewRecorder()
	owner := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}
	a.handler(owner).ServeHTTP(w, httptest.NewRequest("GET", "/v1/terminal?box=s&cols=80&rows=24", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("box status %d", w.Code)
	}
	s.problem = nil
	server := httptest.NewServer(a.handler(owner))
	defer server.Close()
	ctx := context.Background()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/terminal?host=test&cols=80&rows=24"
	_, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://example.com"}}})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: response %v, error %v", response, err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/terminal?host=test&cols=80&rows=24", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var output string
	for !strings.Contains(output, "24 80") {
		kind, b, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary {
			t.Fatalf("initial output: %q %v", b, err)
		}
		output += string(b)
	}
	if !strings.Contains(output, "term=xterm-256color") {
		t.Fatalf("terminal output %q has no TERM", output)
	}
	resize, _ := json.Marshal(contract.TerminalResize{Cols: 90, Rows: 30})
	if err := conn.Write(ctx, websocket.MessageText, resize); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	for {
		kind, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageText {
			var exit contract.TerminalExit
			if err := json.Unmarshal(b, &exit); err != nil || exit.ExitCode != 3 {
				t.Fatalf("exit %s: %v", b, err)
			}
			break
		}
		output += string(b)
	}
	if !strings.Contains(output, "30 90") || !strings.Contains(output, "hello") {
		t.Fatalf("output %q", output)
	}
}

func TestTerminalCloseStopsCommand(t *testing.T) {
	s := &terminalService{argv: []string{"sh", "-c", "echo ready; exec cat"}, done: make(chan struct{})}
	server := httptest.NewServer(New(s, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}))
	defer server.Close()
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/terminal?host=test&cols=80&rows=24", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	conn.CloseNow()
	<-s.done
}
