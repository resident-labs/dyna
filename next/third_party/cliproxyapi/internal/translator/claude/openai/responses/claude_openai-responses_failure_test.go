package responses

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeResponsesFailureRetainsPartialTextAndUsage(t *testing.T) {
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_fixture","usage":{"input_tokens":8,"cache_read_input_tokens":4}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial text"}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`,
		`{"type":"message_stop"}`,
		`{"type":"error","error":{"type":"wrong"}}`,
	}
	assertResponse := func(response gjson.Result) {
		t.Helper()
		for path, want := range map[string]string{"id": "msg_fixture", "status": "failed", "error.code": "overloaded_error", "error.message": "try later", "model": "claude-fixture", "usage.total_tokens": "12", "output.0.status": "incomplete", "output.0.content.0.text": "partial text"} {
			if got := response.Get(path).String(); got != want {
				t.Errorf("%s = %q, want %q: %s", path, got, want, response.Raw)
			}
		}
	}
	var state any
	var failed int
	for _, frame := range frames {
		for _, event := range ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude-fixture", nil, nil, []byte("data: "+frame), &state) {
			_, data, _ := bytes.Cut(event, []byte("\ndata:"))
			root := gjson.ParseBytes(bytes.TrimSpace(data))
			switch root.Get("type").String() {
			case "response.failed":
				failed++
				assertResponse(root.Get("response"))
			case "response.completed":
				t.Fatal("completed after failure")
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failure count = %d", failed)
	}
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(t.Context(), "claude-fixture", nil, nil, []byte("data: "+strings.Join(frames, "\n\ndata: ")+"\n\n"), nil)
	assertResponse(gjson.ParseBytes(out))
	out = ConvertClaudeResponseToOpenAIResponsesNonStream(t.Context(), "claude-fixture", nil, nil, []byte(`{"type":"error"}`), nil)
	if result := gjson.ParseBytes(out); result.Get("error.code").String() != "api_error" || result.Get("created_at").Int() <= 0 {
		t.Fatalf("raw error = %s", out)
	}
}

func TestClaudeResponsesErrorInterruptsNamespacedPatch(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	var state any
	feed := func(frame []byte) []gjson.Result {
		return applyPatchClaudeEvents(t, ConvertClaudeResponseToOpenAIResponses(t.Context(), "claude-fixture", request, nil, frame, &state))
	}
	feed(applyPatchClaudeStart(0, "call_fixture", "editor__apply_patch"))
	feed(applyPatchClaudeFragment(0, `{"input":"*** Begin Patch`))
	events := feed([]byte(`data: {"type":"error","error":{"type":"overloaded_error"}}`))
	var failed gjson.Result
	for _, event := range events {
		switch event.Get("type").String() {
		case "response.custom_tool_call_input.done":
			t.Fatalf("interrupted patch completed: %s", event.Raw)
		case "response.failed":
			failed = event.Get("response")
		}
	}
	item := failed.Get("output.0")
	if failed.Get("error.code").String() != "overloaded_error" || item.Get("namespace").String() != "editor" || item.Get("name").String() != "apply_patch" || item.Get("status").String() != "incomplete" || item.Get("call_id").String() != "call_fixture" {
		t.Fatalf("interrupted patch lost identity or provider error: %s", failed.Raw)
	}
}
