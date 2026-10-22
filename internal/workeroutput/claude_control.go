package workeroutput

import (
	"encoding/json"
	"strings"
)

// Claude Code 2.1.299 reads these stdin lines. The SDK writes the same
// objects: request_id, type control_request, then request. One JSON object
// per line. The CLI answers with one control_response that echoes request_id.

// ClaudeControlInterrupt asks the CLI to stop the current turn. It does not
// cancel commands that are only queued; the caller still owns the process.
func ClaudeControlInterrupt(requestID string) []byte {
	return claudeControlLine(requestID, struct {
		Subtype string `json:"subtype"`
	}{Subtype: "interrupt"})
}

// ClaudeControlSetModel asks the CLI to use model for the following turns.
// An empty model is not a request; the caller skips it. "default" is a real
// value the CLI accepts, so it is sent unchanged.
func ClaudeControlSetModel(requestID, model string) []byte {
	return claudeControlLine(requestID, struct {
		Subtype string `json:"subtype"`
		Model   string `json:"model"`
	}{Subtype: "set_model", Model: model})
}

func claudeControlLine(requestID string, request any) []byte {
	b, err := json.Marshal(struct {
		RequestID string `json:"request_id"`
		Type      string `json:"type"`
		Request   any    `json:"request"`
	}{RequestID: requestID, Type: "control_request", Request: request})
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// ClaudeControlResponse reports the request_id on one stdout control_response.
// ok is false when the line is not that record. success is false when the
// CLI rejected the request.
func ClaudeControlResponse(line string) (requestID string, success, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return "", false, false
	}
	var env struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(line), &env) != nil || env.Type != "control_response" || env.Response.RequestID == "" {
		return "", false, false
	}
	return env.Response.RequestID, env.Response.Subtype == "success", true
}
