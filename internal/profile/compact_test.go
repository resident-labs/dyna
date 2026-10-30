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

// Claude Code and Pi sessions at the context floor are worth an idle
// compaction, whether or not the profile sets a threshold. A smaller context and
// a harness with no compact command are not.
func TestIdleCompactDueAtTheFloorForHarnessesWithACompactCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness string
		context int64
		want    bool
	}{
		{"claude at the floor", HarnessClaudeCode, IdleCompactMinTokens, true},
		{"pi at the floor", HarnessPi, IdleCompactMinTokens, true},
		{"below the floor", HarnessClaudeCode, IdleCompactMinTokens - 1, false},
		{"no compact command", HarnessMock, 400000, false},
	} {
		if got := IdleCompactDue(Profile{Harness: tc.harness}, tc.context); got != tc.want {
			t.Errorf("%s: IdleCompactDue = %t, want %t", tc.name, got, tc.want)
		}
	}
}
