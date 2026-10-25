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
		{HarnessPi, Compact{Tokens: 95000}, nil, nil},
	} {
		c := tc.compact
		args, env, err := CompactNative(Profile{Harness: tc.harness, Model: "m", Compact: &c})
		if err != nil || !reflect.DeepEqual(args, tc.args) || !reflect.DeepEqual(env, tc.env) {
			t.Errorf("%s: args=%q env=%v err=%v", tc.harness, args, env, err)
		}
	}
	if args, env, err := CompactNative(Profile{Harness: HarnessClaudeCode}); args != nil || env != nil || err != nil {
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

// Pi takes no setting; Dyna runs its compact command once the last call's
// prompt reaches the threshold.
func TestCompactDueOnlyForTriggerHarnesses(t *testing.T) {
	p := Profile{Harness: HarnessPi, Model: "m", Compact: &Compact{Tokens: 95000}}
	if args, env, err := CompactNative(p); args != nil || env != nil || err != nil {
		t.Fatalf("pi native = %q %v %v", args, env, err)
	}
	if CompactDue(p, 94999) || !CompactDue(p, 95000) {
		t.Fatal("pi due at the wrong size")
	}
	if CompactDue(Profile{Harness: HarnessClaudeCode, Compact: &Compact{Tokens: 100000}}, 200000) || CompactDue(Profile{Harness: HarnessPi}, 100) {
		t.Fatal("due without a trigger harness or a threshold")
	}
}
