package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestProfilesGlobalModelListsAreIndependent(t *testing.T) {
	store := &profile.Store{Profiles: []profile.Profile{
		{Name: "chain", Harness: profile.HarnessMock, Model: "first", Models: []string{"first", "second"}},
		{Name: "legacy", Harness: profile.HarnessMock, Model: "legacy-model"},
	}}
	result, err := executeTest(context.Background(), Options{
		ScriptSrc: `const models = profiles.map(p => Array.from(p.models)); profiles[0].models[0] = "changed"; return models;`,
		Store:     store, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || strings.Join(got[0], ",") != "first,second" || strings.Join(got[1], ",") != "legacy-model" {
		t.Fatalf("models: %s", result)
	}
	if store.Profiles[0].Models[0] != "first" {
		t.Fatal("JavaScript changed the stored model list")
	}
}
