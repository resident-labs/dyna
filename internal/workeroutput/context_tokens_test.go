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
