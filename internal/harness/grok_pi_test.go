package harness

import (
	"os"
	"path/filepath"
	"testing"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
)

// Grok runs over ACP; its argv-policy tests live in grok_acp_test.go. These
// cover the pi rpc invocation that shares this file's source.
func TestPiReservedFlagsAndLeafLaunch(t *testing.T) {
	for _, flag := range []string{"--mode=text", "--print", "-p", "--session", "--session-id", "--no-session", "--resume", "--continue", "--fork", "--model=x", "--export", "--compaction-trigger-tokens=1"} {
		if _, err := buildInvocation(profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{flag}}, "task"); err == nil {
			t.Errorf("pi accepted reserved %s", flag)
		}
	}
	p := profile.Profile{Harness: profile.HarnessPi, SafeMode: true}
	inv, err := buildInvocationWithExecution(p, "-task", ExecutionOptions{DisableNativeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("exact-id", "next")} {
		if hasArg(spec.argv, "--approve") {
			t.Fatalf("SafeMode bypass: %v", spec.argv)
		}
		if !hasArg(spec.argv, "--no-extensions") {
			t.Fatalf("leaf control missing: %v", spec.argv)
		}
	}
	if _, err := buildInvocationWithExecution(profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"-e", "plugin.js"}}, "task", ExecutionOptions{DisableNativeSubagents: true}); err == nil {
		t.Fatal("Pi leaf accepted explicit extension")
	}
}

func TestPiResumeAndCompactUseSessionID(t *testing.T) {
	storetest.Isolate(t)
	home, err := harnessruntime.NewAccounts().HarnessHome(profile.HarnessPi)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-4000-8000-000000000001"
	path := filepath.Join(sessionDir, "20260101-000000_"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessPi}, "task")
	if err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]commandSpec{
		"resume":  inv.resume(id, "next"),
		"compact": inv.compact(id),
	} {
		if !hasOptionValue(spec.argv, "--session", id) {
			t.Fatalf("%s did not use the session id: %v", name, spec.argv)
		}
	}
}

func TestPiCompactionTriggerFlag(t *testing.T) {
	// One process-wide flag covers every model of the chain, fallbacks
	// included, and no other profile's settings can change it.
	withTarget := profile.Profile{Harness: profile.HarnessPi, Model: "openai-codex/gpt-6.1-terra", Compact: &profile.Compact{Tokens: 200000}}
	inv, err := buildInvocation(withTarget, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("00000000-0000-4000-8000-000000000001", "next"), inv.compact("00000000-0000-4000-8000-000000000001")} {
		if !hasOptionValue(spec.argv, "--compaction-trigger-tokens", "200000") {
			t.Fatalf("compaction trigger missing from %v", spec.argv)
		}
	}
	without := profile.Profile{Harness: profile.HarnessPi, Model: "openai-codex/gpt-6.1-terra"}
	inv, err = buildInvocation(without, "task")
	if err != nil {
		t.Fatal(err)
	}
	if hasArg(inv.initial.argv, "--compaction-trigger-tokens") {
		t.Fatalf("profile without a target got a trigger: %v", inv.initial.argv)
	}
}

func TestPiRuntimeSessionDirectorySurvivesResume(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, "sessions")
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"--session-dir", sessionDir}}, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("00000000-0000-4000-8000-000000000001", "next")} {
		if !hasOptionValue(spec.argv, "--session-dir", sessionDir) {
			t.Fatalf("private session directory missing: %v", spec.argv)
		}
	}
}

// A profile Dyna prepared carries the runtime-owned config file flags in
// ExtraArgs; the invocation must accept them and still reject the same flags
// when the user supplied them.
func TestPiInvocationAcceptsRuntimeOwnedConfigFlags(t *testing.T) {
	prepared := profile.Profile{
		Harness:               profile.HarnessPi,
		ExtraArgs:             []string{"--session-dir", t.TempDir(), "--settings", "s.json", "--mcp-config", "m.json", "--models-file", "models.json"},
		RuntimePermissionArgs: []string{},
	}
	if _, err := buildInvocation(prepared, "task"); err != nil {
		t.Fatal(err)
	}
	user := profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"--settings", "own.json"}, RuntimePermissionArgs: []string{"--settings", "own.json"}}
	if _, err := buildInvocation(user, "task"); err == nil {
		t.Fatal("pi accepted a user-supplied --settings")
	}
	bare := profile.Profile{Harness: profile.HarnessPi, ExtraArgs: []string{"--models-file", "own.json"}}
	if _, err := buildInvocation(bare, "task"); err == nil {
		t.Fatal("pi accepted a user-supplied --models-file on an unprepared profile")
	}
}
