package dynamcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestStreamCancellationAndTypedErrors(t *testing.T) {
	started := make(chan struct{})
	server, err := New(func(ctx context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodSessionStart {
			t.Errorf("method = %s", method)
		}
		close(started)
		<-ctx.Done()
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "retry with clientId"}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dyna/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"session","arguments":{"action":"start","room":"r","name":"n","profile":"p"},"_meta":{"progressToken":"token"}}}`))
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { server.ServeHTTP(w, req); close(done) }()
	<-started
	cancel := httptest.NewRequest(http.MethodPost, "/dyna/", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`))
	server.ServeHTTP(httptest.NewRecorder(), cancel)
	<-done
	body := w.Body.String()
	if !strings.Contains(body, `"progressToken":"token"`) || !strings.Contains(body, `"isError":true`) {
		t.Fatal(body)
	}
}

func TestOperationActionForwardsReplayKey(t *testing.T) {
	server, err := New(func(_ context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodOpSubmit {
			t.Fatalf("method = %s", method)
		}
		var req struct {
			ClientID  string `json:"clientId"`
			Operation struct {
				Kind contract.OpKind     `json:"kind"`
				Args contract.PrOpenArgs `json:"args"`
			} `json:"operation"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.ClientID != "key" || req.Operation.Kind != "pr.open" || req.Operation.Args.Title != "title" {
			t.Fatalf("%+v", req)
		}
		return []byte(`{"ok":true}`), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"action":"open","title":"title","base":"main","clientId":"key"}`), &args)
	result := server.call(context.Background(), callParams{Name: "pr", Arguments: args})
	if result.IsError {
		t.Fatalf("%+v", result)
	}
}

func TestActionDecodeIsStrict(t *testing.T) {
	server, err := New(func(context.Context, contract.Method, []byte) ([]byte, *contract.Error) {
		t.Fatal("forwarded invalid action")
		return nil, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"action":"stop","name":"wrong"}`), &args)
	res := server.call(context.Background(), callParams{Name: "session", Arguments: args})
	if !res.IsError || !strings.Contains(string(res.StructuredContent), "invalid") {
		t.Fatalf("%+v", res)
	}
}
