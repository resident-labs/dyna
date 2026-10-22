package workeroutput

import "testing"

// Shapes come from agy 1.4.2 and pi 1.3.0 captures. A per-call record gives
// the call's prompt size; a process total does not.
func TestContextTokensComeFromOneModelCall(t *testing.T) {
	for _, tc := range []struct {
		dialect, line string
		want          int64
	}{
		{DialectAntigravityStreamJSON, `{"event":"step_update","step_update":{"conversation_id":"c","step_index":10,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":2244,"output_tokens":32,"cache_read_tokens":52598,"total_tokens":2276}}}`, 54842},
		{DialectAntigravityStreamJSON, `{"event":"result","result":{"conversation_id":"c","status":"SUCCESS","response":"x","usage":{"input_tokens":73348,"cache_read_tokens":52598}}}`, 0},
		{DialectPiJSON, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1200,"output":10,"cacheRead":30000,"cacheWrite":500}}}`, 31700},
		{DialectPiJSON, `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, 0},
	} {
		if got := DecodeLine(&Declaration{Dialect: tc.dialect}, tc.line).ContextTokens; got != tc.want {
			t.Errorf("%s %.40s: ContextTokens = %d, want %d", tc.dialect, tc.line, got, tc.want)
		}
	}
}
