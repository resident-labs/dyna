package workflowsvc

import (
	"slices"
	"testing"
)

func TestShellRunnerEnvironmentKeepsRunAndScriptTempValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", "/run/set/by/the/run")
	r := &shellRunner{owner: "ses_20261026-100000-aaaaaaaa"}
	env, err := r.environment([]string{"TEMP=/from/script", "FOO=bar"}, r.owner)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "FOO=bar") || !slices.Contains(env, "TEMP=/from/script") || !slices.Contains(env, "TMPDIR=/run/set/by/the/run") {
		t.Fatalf("env = %q, want the script's pairs and the run's TMPDIR", env)
	}
}
