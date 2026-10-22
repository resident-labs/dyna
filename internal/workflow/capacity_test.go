package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestProfilesRemainingIsLiveReadOnlyRunBudget(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{
		{
			Name: "capped", Harness: profile.HarnessMock, Default: true,
			Taste: 5, Intelligence: 5, Cost: 10, MaxCallsPerRun: 3,
		},
		{
			Name: "unlimited", Harness: profile.HarnessMock,
			Taste: 5, Intelligence: 5, Cost: 10,
		},
	}}
	const script = `
const capped = profiles.find(p => p.name === "capped");
const unlimited = profiles.find(p => p.name === "unlimited");
const initial = capped.remaining;
const first = agent("first", {profile: capped.name});
const afterFirst = capped.remaining;
const second = agent("second", {profile: capped.name});
const afterSecond = capped.remaining;
await Promise.all([first, second]);
const afterComplete = capped.remaining;
capped.remaining = 99;
return {
  initial,
  afterFirst,
  afterSecond,
  afterComplete,
  afterWrite: capped.remaining,
  readOnly: Object.getOwnPropertyDescriptor(capped, "remaining").set === undefined,
  unlimited: unlimited.remaining,
};
`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := Execute(ctx, Options{ScriptSrc: script, Store: store, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var got struct {
		Initial       int  `json:"initial"`
		AfterFirst    int  `json:"afterFirst"`
		AfterSecond   int  `json:"afterSecond"`
		AfterComplete int  `json:"afterComplete"`
		AfterWrite    int  `json:"afterWrite"`
		ReadOnly      bool `json:"readOnly"`
		Unlimited     any  `json:"unlimited"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("unmarshal result %q: %v", result, err)
	}
	if got.Initial != 3 || got.AfterFirst != 2 || got.AfterSecond != 1 || got.AfterComplete != 1 || got.AfterWrite != 1 {
		t.Fatalf("remaining values = %#v, want 3,2,1,1,1", got)
	}
	if !got.ReadOnly || got.Unlimited != nil {
		t.Fatalf("remaining metadata = %#v, want read-only capped value and null unlimited value", got)
	}
}
