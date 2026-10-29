package computers

import (
	"slices"
	"testing"
)

func TestMergeEnvironmentDropsEngineSecrets(t *testing.T) {
	base := []string{"PATH=/usr/bin", "GH_TOKEN=must-not-reach-computers", "ANTHROPIC_API_KEY=must-not-reach-computers", "HOME=/home/owner"}
	got := mergeEnvironment(base, map[string]string{"DISPLAY": ":1"})
	slices.Sort(got)
	want := []string{"DISPLAY=:1", "HOME=/home/owner", "PATH=/usr/bin"}
	if !slices.Equal(got, want) {
		t.Fatalf("computer environment = %q, want %q", got, want)
	}
}
