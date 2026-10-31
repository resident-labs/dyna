package dynamcp

import (
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/box/post"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		var request contract.SessionStartRequest
		if err := json.Unmarshal(body, &request); err != nil || request.ClientID != "retry" {
			t.Errorf("session replay key: %+v, %v", request, err)
		}
		close(started)
		<-ctx.Done()
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "retry with clientId"}
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dyna/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"session","arguments":{"action":"start","room":"r","name":"n","profile":"p","clientId":"retry"},"_meta":{"progressToken":"token"}}}`))
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
	}, nil, nil)
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
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"action":"stop","name":"wrong"}`), &args)
	res := server.call(context.Background(), callParams{Name: "session", Arguments: args})
	if !res.IsError || !strings.Contains(string(res.StructuredContent), "invalid") {
		t.Fatalf("%+v", res)
	}
	_ = json.Unmarshal([]byte(`{"action":"missing"}`), &args)
	res = server.call(context.Background(), callParams{Name: "session", Arguments: args})
	if !res.IsError || !strings.Contains(string(res.StructuredContent), "unknown_action") || !strings.Contains(string(res.StructuredContent), "start") {
		t.Fatalf("%+v", res)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostPreparesLocalAttachments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/files" || r.URL.Query().Get("name") != "report.txt" || string(data) != "report" {
			t.Fatalf("upload: %s %s %q", r.Method, r.URL, data)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"name":"report.txt","sha256":"hash"}`)), Header: make(http.Header)}, nil
	})}
	var sent contract.PostRequest
	server, err := New(func(_ context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodPost {
			t.Fatalf("method %s", method)
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		return []byte(`{"event":"e"}`), nil
	}, nil, func(ctx context.Context, path string) (contract.Attachment, error) {
		return post.UploadHTTP(ctx, client, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(struct {
		Action   string   `json:"action"`
		Text     string   `json:"text"`
		Paths    []string `json:"paths"`
		ClientID string   `json:"clientId"`
	}{"send", "hello", []string{path}, "retry"})
	var args map[string]json.RawMessage
	_ = json.Unmarshal(arguments, &args)
	res := server.call(context.Background(), callParams{Name: "post", Arguments: args})
	if res.IsError || sent.ClientID != "retry" || len(sent.Attachments) != 1 || sent.Attachments[0].SHA256 != "hash" {
		t.Fatalf("result %+v post %+v", res, sent)
	}
}
