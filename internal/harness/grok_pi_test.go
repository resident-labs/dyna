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
	for _, flag := range []string{"--mode=text", "--print", "-p", "--session", "--session-id", "--no-session", "--resume", "--continue", "--fork", "--model=x", "--export"} {
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
