package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const dynaCodemodePayload = `{
	"model":"kimi-k3.1-pro",
	"tools":[{"type":"custom","name":"codemode","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}],
	"tool_choice":{"type":"custom","name":"codemode"},
	"input":[
		{"type":"message","role":"user","content":"run"},
		{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"codemode","input":"text(0)"},
		{"type":"custom_tool_call_output","id":"ctco_1","call_id":"call_1","output":"0"}
	]
}`

func dynaKimiRun(t *testing.T, stream bool, upstream string) (sent []byte, client []byte) {
	t.Helper()
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		sent, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(upstream))}, nil
	}))
	payload := []byte(dynaCodemodePayload)
	if stream {
		payload = []byte(strings.Replace(dynaCodemodePayload, `"model":`, `"stream":true,"model":`, 1))
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding"}, Metadata: map[string]any{"access_token": "test"}}
	req := cliproxyexecutor.Request{Model: "kimi-k3.1-pro", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: stream}
	executor := NewKimiExecutor(&config.Config{})
	if !stream {
		response, err := executor.Execute(ctx, auth, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		return sent, response.Payload
	}
	result, err := executor.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		client = append(client, chunk.Payload...)
	}
	return sent, client
}

func checkDynaCodemodeUpstream(t *testing.T, sent []byte) {
	t.Helper()
	for path, want := range map[string]string{
		"tools.0.type":      "function",
		"tools.0.name":      "codemode",
		"tool_choice.type":  "function",
		"tool_choice.name":  "codemode",
		"input.1.type":      "function_call",
		"input.1.name":      "codemode",
		"input.1.arguments": `{"input":"text(0)"}`,
		"input.2.type":      "function_call_output",
		"input.2.output":    "0",
		"input.2.call_id":   "call_1",
	} {
		if got := gjson.GetBytes(sent, path).String(); got != want {
			t.Errorf("upstream %s = %q, want %q: %s", path, got, want, sent)
		}
	}
	if gjson.GetBytes(sent, "tools.0.format").Exists() {
		t.Errorf("grammar format reached upstream: %s", sent)
	}
}

func TestDynaKimiCodemodeBridge(t *testing.T) {
	sent, client := dynaKimiRun(t, false, `{"id":"resp_1","object":"response","status":"completed","output":[{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_2","name":"codemode","arguments":"{\"input\":\"text(1)\"}"}]}`)
	checkDynaCodemodeUpstream(t, sent)
	if got := gjson.GetBytes(client, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("client item type = %q, want custom_tool_call: %s", got, client)
	}
	if got := gjson.GetBytes(client, "output.0.input").String(); got != "text(1)" {
		t.Fatalf("client input = %q, want text(1): %s", got, client)
	}
	if got := gjson.GetBytes(client, "output.0.name").String(); got != "codemode" {
		t.Fatalf("client name = %q", got)
	}
}

func TestDynaKimiCodemodeBridgeStream(t *testing.T) {
	done := `{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_2","name":"codemode","arguments":"{\"input\":\"text(1)\"}"}`
	sent, client := dynaKimiRun(t, true, strings.Join([]string{
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"codemode\",\"arguments\":\"\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_2\",\"delta\":\"{\\\"input\\\":\\\"text(1)\\\"}\"}\n\n",
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":" + done + "}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_2\",\"output\":[" + done + "]}}\n\n",
		"data: [DONE]\n\n",
	}, ""))
	checkDynaCodemodeUpstream(t, sent)
	out := string(client)
	if !strings.Contains(out, `"type":"custom_tool_call"`) || !strings.Contains(out, `"input":"text(1)"`) || strings.Contains(out, "response.function_call_arguments") {
		t.Fatalf("stream did not return custom_tool_call with the original input: %s", out)
	}
}
