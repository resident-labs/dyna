package workeroutput

import "testing"

// Shapes come from pi 1.3.0 captures. A per-call record gives the call's
// prompt size; a process total does not.
func TestContextTokensComeFromOneModelCall(t *testing.T) {
	for _, tc := range []struct {
		dialect, line string
		want          int64
	}{
		{DialectPiJSON, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1200,"output":10,"cacheRead":30000,"cacheWrite":500}}}`, 31700},
		{DialectPiJSON, `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, 0},
	} {
		if got := DecodeLine(&Declaration{Dialect: tc.dialect}, tc.line).ContextTokens; got != tc.want {
			t.Errorf("%s %.40s: ContextTokens = %d, want %d", tc.dialect, tc.line, got, tc.want)
		}
	}
}

// A per-call usage keeps each count the harness reported, and a count it did
// not report stays nil instead of zero.
func TestCallUsageKeepsUnreportedCountsUnknown(t *testing.T) {
	pi := DecodeLine(&Declaration{Dialect: DialectPiJSON}, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1200,"output":10,"cacheRead":30000,"cacheWrite":0}}}`)
	if pi.Call == nil || pi.Call.Input == nil || *pi.Call.Input != 1200 || pi.Call.CacheRead == nil || *pi.Call.CacheRead != 30000 {
		t.Fatalf("Pi call = %+v, want input 1200 and cache read 30000", pi.Call)
	}
	if pi.Call.CacheWrite == nil || *pi.Call.CacheWrite != 0 || pi.Call.Context == nil || *pi.Call.Context != 31200 {
		t.Fatalf("Pi call = %+v, want reported zero cache write and context 31200", pi.Call)
	}

	partial := DecodeLine(&Declaration{Dialect: DialectPiJSON}, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1200,"output":10}}}`)
	if partial.Call == nil || partial.Call.CacheRead != nil || partial.Call.CacheWrite != nil || partial.Call.Context != nil {
		t.Fatalf("partial Pi call = %+v, want unknown cache counts and context", partial.Call)
	}

	claude := DecodeLine(&Declaration{Dialect: DialectClaudeStreamJSON}, `{"type":"result","usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":0,"output_tokens":5}}`)
	if claude.Call != nil {
		t.Fatalf("Claude result call = %+v, want nil because the result is a whole turn", claude.Call)
	}
}
