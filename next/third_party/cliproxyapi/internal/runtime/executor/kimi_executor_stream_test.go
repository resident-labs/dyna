package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	kimiChatStreamRequest     = `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	kimiChatPartial           = "data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"model\":\"k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n"
	kimiChatFinish            = "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"
	kimiChatUsage             = "data: {\"id\":\"chat_1\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n"
	kimiResponsesTerminalLine = `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":14,"output_tokens":6,"total_tokens":20},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"codemode","arguments":"{\"input\":\"text(1)\"}"}]}}`
)

type kimiStreamUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *kimiStreamUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c.records != nil && record.AuthID == c.authID {
		c.records <- record
	}
}

func runKimiHTTPStream(t *testing.T, from, to sdktranslator.Format, payload string, handler http.HandlerFunc) (<-chan cliproxyexecutor.StreamChunk, context.CancelFunc, <-chan usage.Record) {
	t.Helper()
	capture := &kimiStreamUsageCapture{authID: t.Name(), records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), &kimiStreamUsageCapture{}) })
	upstreamPath := "/v1/chat/completions"
	switch from {
	case sdktranslator.FormatOpenAIResponse:
		upstreamPath = "/v1/responses"
	case sdktranslator.FormatClaude:
		upstreamPath = "/v1/messages"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != upstreamPath {
			t.Errorf("upstream route = %q, want %q", r.URL.Path, upstreamPath)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	auth := &cliproxyauth.Auth{ID: capture.authID, Provider: "kimi", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: from, ResponseFormat: to, Stream: true}
	result, err := NewKimiExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	return result.Chunks, cancel, capture.records
}

func kimiUsageRecord(t *testing.T, records <-chan usage.Record, failed bool, input, output int64) usage.Record {
	t.Helper()
	select {
	case record := <-records:
		if record.Failed != failed || record.Detail.InputTokens != input || record.Detail.OutputTokens != output || record.Detail.TotalTokens != input+output {
			t.Fatalf("usage failed=%t, detail=%+v, fail=%+v; want failed=%t and %d+%d tokens", record.Failed, record.Detail, record.Fail, failed, input, output)
		}
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("missing usage record")
		return usage.Record{}
	}
}

func TestKimiChatStreamBoundaries(t *testing.T) {
	const done = "data: [DONE]\n\n"
	for _, tc := range []struct {
		name, tail, errText string
		status              int
		truncated           bool
	}{
		{name: "done", tail: done},
		{name: "done_at_eof", tail: "data: [DONE]"},
		{name: "empty_keepalive", tail: ": heartbeat\n\ndata: \n\n" + done},
		{name: "finish_without_done", tail: kimiChatFinish},
		{name: "primary_after_secondary", tail: "data: {\"choices\":[{\"index\":1,\"finish_reason\":null},{\"index\":0,\"finish_reason\":\"stop\"}]}\n\n"},
		{name: "finish_at_eof", tail: strings.TrimRight(kimiChatFinish, "\n")},
		{name: "truncated", status: 502, errText: "before [DONE]", truncated: true},
		{name: "secondary_choice", tail: "data: {\"choices\":[{\"index\":1,\"finish_reason\":\"stop\"}]}\n\n", status: 502, errText: "before [DONE]", truncated: true},
		{name: "malformed_after_finish", tail: kimiChatFinish + "data: {\"choices\":", status: 502, errText: "incomplete SSE data frame", truncated: true},
		{name: "premature_done_in_frame", tail: "data: {\"choices\":\n" + done, status: 502, errText: "incomplete SSE data frame", truncated: true},
		{name: "json_error", tail: "data: {\"error\":{\"message\":\"overloaded\",\"status\":429}}\n\n" + done, status: 429, errText: "overloaded"},
		{name: "bare_json_error", tail: "{\"error\":{\"message\":\"overloaded\"}}\n", status: 502, errText: "overloaded"},
		{name: "multiline_error", tail: "data: {\"error\":\ndata: {\"message\":\"overloaded\"}}\n\n" + done, status: 502, errText: "overloaded"},
		{name: "named_text_error", tail: "event: error\ndata: overloaded\n\n" + done, status: 502, errText: "overloaded"},
		{name: "empty_error_at_eof", tail: "event: response.error", status: 502, errText: "without data"},
		{name: "empty_error_data", tail: "event: response.error\ndata:\n\n", status: 502, errText: "without data"},
		{name: "done_in_error_frame", tail: "event: response.failed\n" + done, status: 502, errText: "before [DONE]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks, _, records := runKimiHTTPStream(t, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, kimiChatStreamRequest, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, kimiChatPartial+tc.tail)
			})
			var output strings.Builder
			var streamErr error
			for chunk := range chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			wantFail := tc.status != 0
			wantStops := 1
			if wantFail {
				wantStops = 0
			}
			if got := strings.Count(output.String(), "event: message_stop\n"); got != wantStops {
				t.Fatalf("unexpected message_stop count %d in %q", got, output.String())
			}
			if !wantFail && streamErr != nil {
				t.Fatal(streamErr)
			}
			if wantFail {
				var e statusErr
				if !errors.As(streamErr, &e) || e.StatusCode() != tc.status || e.IsStreamTruncated() != tc.truncated || !strings.Contains(e.Error(), tc.errText) {
					t.Fatalf("unexpected stream error %#v, want status=%d truncated=%t and %q", streamErr, tc.status, tc.truncated, tc.errText)
				}
			}
			record := kimiUsageRecord(t, records, wantFail, 12, 3)
			if tc.truncated && record.Fail.Code != usage.FailureStreamTruncatedCode {
				t.Errorf("usage failure code = %q", record.Fail.Code)
			}
			if tc.errText == "overloaded" && record.Fail.Body != "upstream stream returned an error payload" {
				t.Errorf("raw upstream error in usage: %+v", record.Fail)
			}
			if tools := record.Detail.ToolUsageDetails; tools == nil || tools.ToolCallsRequested == nil || *tools.ToolCallsRequested != 1 {
				t.Errorf("lost tool call counter: %+v", tools)
			}
		})
	}
}

func TestKimiChatStreamFrameLimits(t *testing.T) {
	for _, tc := range []struct {
		name, wantErr string
		lineBytes     int
		lines         int
	}{
		{name: "multiline_above_old_limit", lineBytes: 1_500_000, lines: 1},
		{name: "oversized_line", lineBytes: helps.StreamScannerBuffer, lines: 1, wantErr: "token too long"},
		{name: "oversized_multiline_frame", lineBytes: helps.StreamScannerBuffer/2 + 1, lines: 2, wantErr: "frame exceeds 50 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks, _, records := runKimiHTTPStream(t, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI, kimiChatStreamRequest, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "data: {\"choices\":[\n")
				padding := strings.Repeat("a", tc.lineBytes)
				for range tc.lines {
					_, _ = io.WriteString(w, "data: {\"ignored\":\"")
					_, _ = io.WriteString(w, padding)
					_, _ = io.WriteString(w, "\"},\n")
				}
				_, _ = io.WriteString(w, "data: {\"index\":0,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			})
			var errStream error
			var payload []byte
			for chunk := range chunks {
				if chunk.Err != nil {
					errStream = chunk.Err
				} else {
					payload = chunk.Payload
				}
			}
			if tc.wantErr != "" {
				if errStream == nil || !strings.Contains(errStream.Error(), tc.wantErr) || len(payload) != 0 {
					t.Fatalf("stream error = %v, payload length = %d, want %q and no payload", errStream, len(payload), tc.wantErr)
				}
				kimiUsageRecord(t, records, true, 0, 0)
				return
			}
			if errStream != nil || len(gjson.GetBytes(payload, "choices.0.ignored").Str) != tc.lineBytes || bytes.Contains(payload, []byte("\n")) {
				t.Fatalf("large multiline JSON not forwarded as one data line (len=%d, err=%v)", len(payload), errStream)
			}
		})
	}
}

func TestKimiChatStreamCancellation(t *testing.T) {
	for _, afterTerminal := range []bool{false, true} {
		name := "before_terminal"
		if afterTerminal {
			name = "after_translated_message_stop"
		}
		t.Run(name, func(t *testing.T) {
			chunks, cancel, records := runKimiHTTPStream(t, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, kimiChatStreamRequest, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, kimiChatPartial+kimiChatFinish+kimiChatUsage)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			canceled := false
			for chunk := range chunks {
				if !afterTerminal || bytes.HasPrefix(chunk.Payload, []byte("event: message_stop\n")) {
					cancel()
					canceled = true
					break
				}
			}
			if !canceled {
				t.Fatal("stream closed before reaching cancellation boundary")
			}
			// Do not receive pending chunks until the producer has settled cancellation.
			kimiUsageRecord(t, records, !afterTerminal, 12, 3)
			for chunk := range chunks {
				t.Errorf("unexpected chunk after cancellation: %+v", chunk)
			}
		})
	}
}

func TestKimiResponsesStreamTerminalDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, payload, boundary string
		hold, blank, fail       bool
	}{
		{name: "cancel_before_blank", payload: `{"input":"hi"}`, boundary: `"type":"response.completed"`, hold: true, blank: true, fail: true},
		{name: "cancel_before_eof_blank", payload: `{"input":"hi"}`, boundary: `"type":"response.completed"`, fail: true},
		{name: "cancel_after_blank", payload: `{"input":"hi"}`, boundary: "\n", hold: true, blank: true},
		{name: "clean_eof_supplies_blank", payload: `{"input":"hi"}`},
		{name: "cancel_before_bridged_data", payload: dynaCodemodePayload, boundary: "event: response.completed\n", hold: true, fail: true},
		{name: "cancel_after_bridged_frame", payload: dynaCodemodePayload, boundary: `"type":"response.completed"`, hold: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks, cancel, records := runKimiHTTPStream(t, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, tc.payload, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "event: response.completed\n"+kimiResponsesTerminalLine+"\n")
				if tc.blank {
					_, _ = io.WriteString(w, "\n")
				}
				w.(http.Flusher).Flush()
				if tc.hold {
					<-r.Context().Done()
				}
			})
			var last string
			matched := false
			for chunk := range chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				last = string(chunk.Payload)
				match := tc.boundary != "" && strings.Contains(last, tc.boundary)
				if tc.boundary == "\n" {
					match = last == "\n"
				}
				if match {
					matched = true
					cancel()
					break
				}
			}
			if tc.boundary != "" && !matched {
				t.Fatalf("stream closed before %q was sent", tc.boundary)
			}
			if tc.boundary == "" && last != "\n" {
				t.Fatalf("clean EOF did not provide the terminal delimiter: %q", last)
			}
			if tc.name == "cancel_after_bridged_frame" && (!strings.Contains(last, `"type":"custom_tool_call"`) || !strings.HasSuffix(last, "\n\n")) {
				t.Fatalf("bridged completion not delivered as a complete custom-tool frame: %q", last)
			}
			kimiUsageRecord(t, records, tc.fail, 14, 6)
			for chunk := range chunks {
				t.Errorf("unexpected chunk after cancellation: %+v", chunk)
			}
		})
	}
}

func TestKimiResponsesStreamPreservesStructuredError(t *testing.T) {
	const failure = "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":7,\"response\":{\"error\":{\"status\":429,\"message\":\"overloaded\"}}}\n\n"
	chunks, _, records := runKimiHTTPStream(t, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAIResponse, `{"input":"hi"}`, func(w http.ResponseWriter, _ *http.Request) {
		// A later completion must not turn a structured error into a success record.
		_, _ = io.WriteString(w, failure+kimiResponsesTerminalLine+"\n\n")
	})
	var output strings.Builder
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("structured error should be forwarded without a duplicate synthetic error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.HasPrefix(output.String(), failure) {
		t.Fatalf("structured error was changed: %s", output.String())
	}
	record := kimiUsageRecord(t, records, true, 0, 0)
	if record.Fail.StatusCode != http.StatusTooManyRequests || record.Fail.Body != "upstream stream returned an error payload" {
		t.Fatalf("unexpected failure accounting: %+v", record.Fail)
	}
}

func TestKimiClaudeStreamStillUsesMessagesTerminal(t *testing.T) {
	const start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"k3\",\"usage\":{\"input_tokens\":12}}}\n\n"
	const delta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n"
	const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct{ name, tail string }{{"complete", stop}, {"truncated", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			chunks, _, records := runKimiHTTPStream(t, sdktranslator.FormatClaude, sdktranslator.FormatClaude, `{"stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, start+delta+tc.tail)
			})
			var errStream error
			var output strings.Builder
			for chunk := range chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					errStream = chunk.Err
				}
			}
			if tc.tail == "" {
				if errStream == nil || !strings.Contains(errStream.Error(), "before message_stop") || strings.Contains(output.String(), "event: message_stop") {
					t.Fatalf("Messages EOF should fail without faking message_stop: %v, %s", errStream, output.String())
				}
			} else if errStream != nil || !strings.Contains(output.String(), stop) {
				t.Fatalf("Messages terminal event changed: %v, %s", errStream, output.String())
			}
			kimiUsageRecord(t, records, tc.tail == "", 12, 3)
		})
	}
}
