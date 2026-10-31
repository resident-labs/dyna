package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScriptedStreamingCallsAndRecords(t *testing.T) {
	var records bytes.Buffer
	s := &server{script: script{Models: map[string][]step{"fixture": {
		{ID: "first", Calls: []call{{ID: "call-1", Name: "post", Arguments: json.RawMessage(`{"text":"hi"}`)}}, InputTokens: 12, OutputTokens: 3},
		{ID: "second", Text: "done"},
	}}}, next: make(map[string]int), records: json.NewEncoder(&records)}
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"fixture","stream":true,"tools":[{"name":"post"}]}`)))
		return w
	}
	first := request()
	if first.Code != 200 || !strings.Contains(first.Body.String(), `"name":"post"`) || !strings.Contains(first.Body.String(), `"finish_reason":"tool_calls"`) || !strings.Contains(first.Body.String(), `"prompt_tokens":12`) {
		t.Fatalf("first response: %d %s", first.Code, first.Body.String())
	}
	second := request()
	if second.Code != 200 || !strings.Contains(second.Body.String(), `"content":"done"`) {
		t.Fatalf("second response: %s", second.Body.String())
	}
	if exhausted := request(); exhausted.Code != 400 {
		t.Fatalf("exhausted status: %d", exhausted.Code)
	}
	hash := sha256.Sum256([]byte(`[{"name":"post"}]`))
	decoder := json.NewDecoder(&records)
	var got record
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Fixture != "fixture" || got.Step != "first" || got.Request != 1 || got.ToolsSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("record: %+v", got)
	}
	if err := decoder.Decode(&got); err != nil || got.Step != "second" || got.Request != 2 {
		t.Fatalf("second record: %+v, %v", got, err)
	}
}
