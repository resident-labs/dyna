package profile

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

// A Claude Code session that sat idle past the cache lifetime compacts before
// its turn when its context is at the floor. A warm cache, a small context, a
// profile without compaction and another harness do not.
func TestIdleCompactDueOnlyForColdLargeClaudeSessions(t *testing.T) {
	claude := Profile{Harness: HarnessClaudeCode, Compact: &Compact{Tokens: 300000}}
	cold := ClaudeCacheLifetime + time.Second
	for _, tc := range []struct {
		name    string
		p       Profile
		context int64
		idle    time.Duration
		want    bool
	}{
		{"cold and large", claude, IdleCompactMinTokens, cold, true},
		{"cache still warm", claude, 400000, ClaudeCacheLifetime, false},
		{"context below the floor", claude, IdleCompactMinTokens - 1, cold, false},
		{"compaction off", Profile{Harness: HarnessClaudeCode}, 400000, cold, false},
		{"other harness", Profile{Harness: HarnessPi, Compact: &Compact{Tokens: 300000}}, 400000, cold, false},
	} {
		if got := IdleCompactDue(tc.p, tc.context, tc.idle); got != tc.want {
			t.Errorf("%s: IdleCompactDue = %t, want %t", tc.name, got, tc.want)
		}
	}
}
