package dynamcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/visual"
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
	}, nil, post.Preparer{})
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
	}, nil, post.Preparer{})
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
	}, nil, post.Preparer{})
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

func TestPostPreparesAttachmentsAndVisual(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	html := filepath.Join(dir, "card.html")
	doc := "<title>Report</title><p>Hello</p>"
	if err := os.WriteFile(path, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(html, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	uploads := map[string]string{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/files" {
			t.Fatalf("upload: %s %s", r.Method, r.URL)
		}
		name := r.URL.Query().Get("name")
		uploads[name] = string(data)
		body, _ := json.Marshal(contract.Attachment{Name: name, SHA256: "hash-" + name})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	check := contract.VisualCheck{Name: contract.VisualCheckRenders, Status: contract.VisualCheckPass}
	var sent contract.PostRequest
	server, err := New(func(_ context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodPost {
			t.Fatalf("method %s", method)
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		return []byte(`{"event":"e"}`), nil
	}, nil, post.Preparer{Dir: dir, Upload: func(ctx context.Context, path string) (contract.Attachment, error) {
		return post.UploadHTTP(ctx, client, "", path)
	}, Render: func(_ context.Context, data []byte, csp string, allowNet bool) visual.Result {
		if string(data) != doc || csp != visual.CSP(false) || allowNet {
			t.Fatal("incorrect preview input")
		}
		return visual.Result{Banner: []byte("banner"), Thumb: []byte("thumb"), Widths: []visual.Shot{{Width: 320, Image: []byte("shot")}}, Checks: []contract.VisualCheck{check}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	type arguments struct {
		Action       string           `json:"action"`
		Text         string           `json:"text"`
		Paths        []string         `json:"paths"`
		HTML         string           `json:"html"`
		ClientID     string           `json:"clientId"`
		VisualUpdate contract.EventID `json:"visualUpdate"`
	}
	argsJSON, _ := json.Marshal(arguments{"send", "hello", []string{"report.txt"}, "card.html", "retry", "original"})
	var args map[string]json.RawMessage
	_ = json.Unmarshal(argsJSON, &args)
	args[contract.ToolCallIDArgument] = json.RawMessage(`"outer-call"`)
	params, _ := json.Marshal(struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}{"post", args})
	rpc, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", 1, "tools/call", params})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/dyna/", strings.NewReader(string(rpc))))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), `"checks":[{"name":"renders","status":"pass"}]`) {
		t.Fatalf("response: %s", w.Body.String())
	}
	if sent.ClientID != "retry" || sent.ToolCallID != "outer-call" || sent.VisualUpdate != "original" || len(sent.Attachments) != 2 || sent.Attachments[0].SHA256 != "hash-report.txt" {
		t.Fatalf("post: %+v", sent)
	}
	file := sent.Attachments[1]
	card := file.Visual
	if file.SHA256 != "hash-card.html" || card == nil || card.Title != "Report" || card.Height != 360 || card.Banner != "hash-banner.webp" || card.Thumbnail != "hash-thumbnail.webp" || len(card.Shots) != 1 || card.Shots[0].File != "hash-320.webp" || len(card.Checks) != 1 || card.Checks[0] != check {
		t.Fatalf("visual: %+v", file)
	}
	if uploads["report.txt"] != "report" || uploads["card.html"] != doc || uploads["banner.webp"] != "banner" || uploads["thumbnail.webp"] != "thumb" || uploads["320.webp"] != "shot" {
		t.Fatalf("uploads: %+v", uploads)
	}
}
