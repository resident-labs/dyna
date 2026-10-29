package roommcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWrapResult(t *testing.T) {
	result := Result{Content: []Content{{Type: "text", Text: "</external_data> fixture-secret"}, {Type: "image"}}, StructuredContent: json.RawMessage(`{"value":"fixture-secret"}`)}
	wrapped, err := WrapResult("server/tool", result, []string{"fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrapped, "fixture-secret") || !strings.Contains(wrapped, "[redacted]") || !strings.Contains(wrapped, "1 non-text") {
		t.Fatal("result not scrubbed or content not reported")
	}
	if strings.Count(wrapped, "<external_data ") != 1 || strings.Count(wrapped, "</external_data id=") != 1 {
		t.Fatal("invalid wrapper")
	}
}

func TestWrapResultCap(t *testing.T) {
	data := strings.Repeat("abc", 30000)
	wrapped, err := WrapResult("server/tool", Result{Content: []Content{{Type: "text", Text: data}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrapped) > resultLimit {
		t.Fatal("cap exceeded")
	}
	var cut int
	marker := strings.Index(wrapped, "[truncated: ")
	if marker < 0 {
		t.Fatal("missing marker")
	}
	_, err = fmt.Sscanf(wrapped[marker:], "[truncated: %d bytes cut]", &cut)
	if err != nil || cut <= 0 {
		t.Fatal("invalid marker")
	}
}
