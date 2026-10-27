package localci

import "testing"

// The engine used to match these keys by prefix. The typed parser must accept
// the same keys, and must reject a near miss that a mistyped prefix would hide.
func TestParseEventKeyGuardsWakeFamilies(t *testing.T) {
	sha := "abc"
	built := []EventKey{
		ModeEventKey(),
		NodeEventKey("hermes"),
		ResultEventKey(sha),
		CachesEventKey("hermes"),
		HostedEventKey(sha),
		WarmEventKey("/repo", "hermes"),
		AttachEventKey(sha),
	}
	for _, key := range built {
		parsed, ok := ParseEventKey(key.String())
		if !ok || parsed != key {
			t.Fatalf("parse %q = %#v, %v", key.String(), parsed, ok)
		}
	}
	if got := ResultEventKey(sha).String(); got != "result:"+sha {
		t.Fatalf("result key = %q", got)
	}
	result, ok := ParseEventKey("result:" + sha)
	if !ok || !result.WakesCI() {
		t.Fatalf("result key did not wake: %#v", result)
	}
	if head, isResult := result.ResultSHA(); !isResult || head != sha {
		t.Fatalf("result sha = %q, %v", head, isResult)
	}
	// A bare family prefix still wakes, as the old prefix check did.
	for _, raw := range []string{"mode", "node:", "result:"} {
		key, ok := ParseEventKey(raw)
		if !ok || !key.WakesCI() {
			t.Fatalf("%q did not wake", raw)
		}
	}
	empty, ok := ParseEventKey("result:")
	if _, isResult := empty.ResultSHA(); !ok || !isResult {
		t.Fatal("empty result key lost its family")
	}
	for _, raw := range []string{"", "model", "node", "result", "resultish:abc", "caches:hermes", "hosted:" + sha, "warm:/repo:hermes", "attach:" + sha} {
		key, ok := ParseEventKey(raw)
		if key.WakesCI() {
			t.Fatalf("%q woke CI workers", raw)
		}
		if raw == "model" || raw == "node" || raw == "result" || raw == "resultish:abc" || raw == "" {
			if ok {
				t.Fatalf("accepted %q", raw)
			}
		}
	}
}
