package workeroutput

import (
	"os"
	"strings"
	"testing"
)

func TestAntigravityCaptureUsage(t *testing.T) {
	b, err := os.ReadFile("testdata/antigravity-usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	decl := &Declaration{Dialect: DialectAntigravityStreamJSON}
	n := NewUsageNormalizer("", nil)
	var live, terminal Usage
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if u, ok := n.Observe(decl, line); ok {
			live.Add(u)
		}
		if u, ok := ExtractUsage(decl, line); ok {
			terminal.Add(u)
		}
	}
	want := Usage{InputTokens: 11950, OutputTokens: 91}
	if live != want || terminal != want {
		t.Fatalf("live=%+v terminal=%+v want=%+v", live, terminal, want)
	}
	c := n.Normalization()
	if c.Input != "complete" || c.Output != "complete" || c.CachedInput != "complete" || c.Cost != "unknown" {
		t.Fatalf("coverage=%+v", c)
	}
}

func TestAntigravityAccounting(t *testing.T) {
	d := &Declaration{Dialect: DialectAntigravityStreamJSON}
	step := `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","usage":{"input_tokens":100,"output_tokens":10,"thinking_tokens":5,"cache_read_tokens":20,"total_tokens":115}}}`
	tests := []struct {
		name  string
		lines []string
		want  Usage
	}{
		{"interrupted after step", []string{step}, Usage{InputTokens: 120, CachedTokens: 20, OutputTokens: 15}},
		{"duplicate step", []string{step, step}, Usage{InputTokens: 120, CachedTokens: 20, OutputTokens: 15}},
		{"terminal remainder", []string{step, `{"event":"result","result":{"usage":{"input_tokens":150,"output_tokens":20,"thinking_tokens":5,"cache_read_tokens":30,"total_tokens":175}}}`}, Usage{InputTokens: 180, CachedTokens: 30, OutputTokens: 25}},
		{"thinking already included", []string{`{"event":"result","result":{"usage":{"input_tokens":100,"output_tokens":15,"thinking_tokens":5,"cache_read_tokens":20,"total_tokens":115}}}`}, Usage{InputTokens: 120, CachedTokens: 20, OutputTokens: 15}},
		{"total absent", []string{`{"event":"result","result":{"usage":{"input_tokens":100,"output_tokens":10,"cache_read_tokens":20}}}`}, Usage{InputTokens: 120, CachedTokens: 20, OutputTokens: 10}},
		{"active step ignored", []string{strings.Replace(step, `"DONE"`, `"ACTIVE"`, 1)}, Usage{}},
		{"invalid metrics", []string{`{"event":"result","result":{"usage":{"input_tokens":-1,"output_tokens":"bad","cache_read_tokens":null}}}`}, Usage{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewUsageNormalizer("resumed", nil)
			var total Usage
			for _, line := range tt.lines {
				if u, ok := n.Observe(d, line); ok {
					total.Add(u)
				}
			}
			if total != tt.want {
				t.Fatalf("got=%+v want=%+v", total, tt.want)
			}
		})
	}
	n := NewUsageNormalizer("", nil)
	first, _ := n.Observe(d, step)
	n.BeginProcess()
	second, _ := n.Observe(d, step)
	if first != second || first.Total() == 0 {
		t.Fatalf("process usage reset: first=%+v second=%+v", first, second)
	}
}

func TestMuseUnavailableUsage(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	u, ok := n.Observe(&Declaration{Dialect: DialectMuseJSONL}, `{"payload_type":"run.terminal.completed","payload":{"text":"done"}}`)
	c := n.Normalization()
	if ok || u.Total() != 0 || c.Input != "unknown" || c.Output != "unknown" || c.CachedInput != "unknown" || c.Cost != "unknown" || c.Reason != Capabilities(DialectMuseJSONL).UsageReason {
		t.Fatalf("usage=%+v ok=%v coverage=%+v", u, ok, c)
	}
}

func TestAntigravityInputIncludesCacheReads(t *testing.T) {
	n := NewUsageNormalizer("resumed", nil)
	u, _ := n.Observe(&Declaration{Dialect: DialectAntigravityStreamJSON}, `{"event":"result","result":{"usage":{"input_tokens":15014,"output_tokens":5203,"cache_read_tokens":299700,"total_tokens":20217}}}`)
	if u.CachedTokens != 299700 || u.InputTokens != 314714 || u.CachedTokens > u.InputTokens {
		t.Fatalf("cache reads must be a subset of input: %+v", u)
	}
}

// Input adds cache reads, so input without a cache report is a lower bound.
func TestAntigravityInputIsPartialWithoutCacheReads(t *testing.T) {
	d := &Declaration{Dialect: DialectAntigravityStreamJSON}
	for _, tt := range []struct{ name, cache, input string }{
		{"missing", ``, "partial"},
		{"null", `,"cache_read_tokens":null`, "partial"},
		{"invalid", `,"cache_read_tokens":"x"`, "partial"},
		{"zero", `,"cache_read_tokens":0`, "complete"},
	} {
		n := NewUsageNormalizer("resumed", nil)
		n.Observe(d, `{"event":"result","result":{"usage":{"input_tokens":100,"output_tokens":10`+tt.cache+`}}}`)
		if got := n.Normalization().Input; got != tt.input {
			t.Errorf("%s: input coverage %q, want %q", tt.name, got, tt.input)
		}
	}
}
