package profile

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompactNativeMapsTokensPerHarness(t *testing.T) {
	for _, tc := range []struct {
		harness string
		compact Compact
		args    []string
		env     map[string]string
	}{
		// Claude Code compacts 33K below autoCompactWindow, so N = 95000 needs W = 128000.
		{HarnessClaudeCode, Compact{Tokens: 95000}, []string{"--settings", `{"autoCompactWindow":128000}`}, nil},
		{HarnessCodex, Compact{Tokens: 95000}, []string{"-c", "model_auto_compact_token_limit=95000"}, nil},
		{HarnessMuse, Compact{Tokens: 95000, Windows: map[string]int64{"m": 200000}}, []string{"-c", "context_compaction.soft_threshold=0.475"}, nil},
		// 95000 of 200000 is 47.5%; Grok gets 47 so it never compacts late.
		{HarnessGrok, Compact{Tokens: 95000, Windows: map[string]int64{"m": 200000}}, nil, map[string]string{"GROK_AUTO_COMPACT_THRESHOLD_PERCENT": "47"}},
		{HarnessOpenCode, Compact{Tokens: 95000}, nil, nil},
	} {
		c := tc.compact
		args, env, err := CompactNative(Profile{Harness: tc.harness, Model: "m", Compact: &c})
		if err != nil || !reflect.DeepEqual(args, tc.args) || !reflect.DeepEqual(env, tc.env) {
			t.Errorf("%s: args=%q env=%v err=%v", tc.harness, args, env, err)
		}
	}
	if args, env, err := CompactNative(Profile{Harness: HarnessCodex}); args != nil || env != nil || err != nil {
		t.Fatalf("nil compact changed the run: %q %v %v", args, env, err)
	}
}

func TestCompactNativeRejectsWhatTheHarnessCannotTake(t *testing.T) {
	for _, tc := range []struct {
		harness string
		compact Compact
		want    string
	}{
		{HarnessClaudeCode, Compact{Tokens: 66999}, "67000 to 967000"},
		{HarnessClaudeCode, Compact{Tokens: 967001}, "67000 to 967000"},
		{HarnessMuse, Compact{Tokens: 95000}, "context window"},
		{HarnessGrok, Compact{Tokens: 1000, Windows: map[string]int64{"m": 200000}}, "1%"},
		{HarnessPi, Compact{Tokens: 0}, "at least 1"},
	} {
		c := tc.compact
		p := Profile{Name: "p", Harness: tc.harness, Model: "m", Taste: 5, Intelligence: 5, Cost: 5, Compact: &c}
		if _, _, err := CompactNative(p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %+v: err = %v, want %q", tc.harness, c, err, tc.want)
		}
		if err := p.Validate(); err == nil {
			t.Errorf("%s %+v: Validate accepted it", tc.harness, c)
		}
	}
}

// Every model of the chain must take the threshold, so a fallback with a
// smaller window is rejected on save and gets its own window at launch.
func TestCompactUsesEachChainModelsWindow(t *testing.T) {
	c := Compact{Tokens: 95000, Windows: map[string]int64{"big": 1000000, "small": 200000}}
	p := Profile{Name: "p", Harness: HarnessGrok, Model: "big", Models: []string{"big", "small"}, Taste: 5, Intelligence: 5, Cost: 5, Compact: &c}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{"big": "9", "small": "47"} {
		p.Model = model
		if _, env, err := CompactNative(p); err != nil || env["GROK_AUTO_COMPACT_THRESHOLD_PERCENT"] != want {
			t.Errorf("%s: env=%v err=%v", model, env, err)
		}
	}
	p.Model, p.Models = "big", []string{"big", "small", "unknown"}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), `"unknown"`) {
		t.Fatalf("a fallback without a window was accepted: %v", err)
	}
}

// Pi and OpenCode take no setting; Dyna runs their compact command once
// the last call's prompt reaches the threshold.
func TestCompactDueOnlyForTriggerHarnesses(t *testing.T) {
	for _, h := range []string{HarnessPi, HarnessOpenCode} {
		p := Profile{Harness: h, Model: "m", Compact: &Compact{Tokens: 95000}}
		if args, env, err := CompactNative(p); args != nil || env != nil || err != nil {
			t.Fatalf("%s native = %q %v %v", h, args, env, err)
		}
		if CompactDue(p, 94999) || !CompactDue(p, 95000) {
			t.Fatalf("%s due at the wrong size", h)
		}
	}
	if CompactDue(Profile{Harness: HarnessCodex, Compact: &Compact{Tokens: 1}}, 100) || CompactDue(Profile{Harness: HarnessPi}, 100) {
		t.Fatal("due without a trigger harness or a threshold")
	}
}

func TestAntigravityACPRefusesManualCompaction(t *testing.T) {
	p := Profile{Harness: HarnessAntigravity, Compact: &Compact{Tokens: 95000}}
	_, _, err := CompactNative(p)
	if err == nil || !strings.Contains(err.Error(), "not supported by Antigravity ACP 1.3.0") {
		t.Fatalf("manual compaction was not refused clearly: %v", err)
	}
	if CompactDue(p, 100000) {
		t.Fatal("unsupported manual compaction became due")
	}
}
