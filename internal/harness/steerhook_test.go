package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/fakeruntime"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

const envTestSteerHookDir = "DYNA_TEST_STEER_HOOK_DIR"

// TestMain lets the test binary act as `dyna hook steer` for fake harness
// CLIs, and keeps every other test on the interrupt path (their fake CLIs
// know nothing about hooks).
func TestMain(m *testing.M) {
	PrepareRuntime = fakeruntime.Prepare
	harnesshealth.VersionDetector = func(context.Context, profile.Profile) harnesshealth.VersionInfo {
		return harnesshealth.VersionInfo{Version: "test", Installed: true}
	}
	healthCache, cacheErr := os.MkdirTemp("", "dyna-health-tests-")
	if cacheErr != nil {
		panic(cacheErr)
	}
	os.Setenv("XDG_CACHE_HOME", healthCache)

	if dir := os.Getenv(envTestSteerHookDir); dir != "" {
		_ = RunSteerHook(dir, os.Stdin, os.Stdout)
		os.Exit(0)
	}
	// The fake pi runs before any DYNA_* reset: its mode variables arrive in
	// its own environment, set through the profile.
	if os.Getenv(envFakeCodex) != "" {
		fakeCodexMain()
		// A race-enabled os.Exit(0) runs the race finalizer, about 1 s, and the
		// processguard helper repeats it when it replays status 0. The fake
		// exits only after its stdin closes, so its status is never read.
		syscall.Exit(7)
	}
	if os.Getenv(envFakePi) != "" {
		fakePiMain()
		os.Exit(0)
	}
	// Keep hook subprocess mode above the reset: it uses a DYNA_* key.
	// Normal tests set their own Dyna identity, journal paths, and options.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	steerHookSupport = func(string, string) bool { return false }
	code := m.Run()
	os.RemoveAll(healthCache)
	os.Exit(code)
}

func TestRunSteerHook(t *testing.T) {
	dir := t.TempDir()
	h := &steerHook{harness: profile.HarnessCodex, dir: dir}
	defer h.cleanup()
	run := func(session string) string {
		var out bytes.Buffer
		in := strings.NewReader(`{"session_id":"` + session + `","hook_event_name":"PostToolUse"}`)
		if err := RunSteerHook(dir, in, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run("s1"); got != "" {
		t.Fatalf("empty handoff dir printed %q", got)
	}
	msgs := []runstore.SteeringMessage{{Message: "stop the refactor"}}
	if err := h.offer("s1", msgs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := run("other"); got != "" || h.taken() != nil {
		t.Fatalf("another session took the message: %q", got)
	}
	var reply struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(run("s1")), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.HookSpecificOutput.HookEventName != "PostToolUse" || !strings.Contains(reply.HookSpecificOutput.AdditionalContext, "stop the refactor") {
		t.Fatalf("reply = %#v", reply)
	}
	if got := h.taken(); len(got) != 1 {
		t.Fatalf("taken() = %v after the hook claimed it", got)
	}
	if got := run("s1"); got != "" {
		t.Fatalf("message delivered twice: %q", got)
	}
	// A withdrawn message is no longer offered.
	if err := h.offer("s1", msgs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if back, delivered := h.withdraw(); len(back) != 1 || delivered != nil {
		t.Fatalf("withdraw() = %v, %v", back, delivered)
	}
	if got := run("s1"); got != "" {
		t.Fatalf("withdrawn message delivered: %q", got)
	}
	if _, _, err := h.retire(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func mustHookArgv(t *testing.T, hook *steerHook, args []string) []string {
	t.Helper()
	out, err := hook.argv(args, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSteerHookArgv(t *testing.T) {
	codex := &steerHook{harness: profile.HarnessCodex, command: `'/bin/dyna' hook steer --dir '/tmp/x'`}
	got := strings.Join(mustHookArgv(t, codex, []string{"codex", "-c", "features.memories=false", "app-server"}), " ")
	want := `codex -c hooks.PostToolUse=[{matcher="*",hooks=[{type="command",command="'/bin/dyna' hook steer --dir '/tmp/x'",timeout=10}]}] --dangerously-bypass-hook-trust -c features.memories=false app-server`
	if got != want {
		t.Fatalf("codex argv:\n got %s\nwant %s", got, want)
	}
	muse := &steerHook{harness: profile.HarnessMuse, dir: t.TempDir(), command: "dyna hook steer"}
	got = strings.Join(mustHookArgv(t, muse, []string{"muse", "exec", "--json", "--session-id", "s", "do it"}), " ")
	path, _ := json.Marshal(filepath.Join(muse.dir, "hooks.json"))
	want = "muse exec -c hooks={} -c managed_hooks_path=" + string(path) + " -c managed_hooks_env_vars=[] --json --session-id s do it"
	if got != want {
		t.Fatalf("muse argv:\n got %s\nwant %s", got, want)
	}
	other := &steerHook{harness: profile.HarnessAntigravity}
	if got := mustHookArgv(t, other, []string{"agy", "--print"}); len(got) != 2 {
		t.Fatalf("antigravity argv changed: %v", got)
	}
	if !museVersionAtLeast("Muse Code 1.4.7 (1.4.7-R6101.1)", museHookMinVersion) || museVersionAtLeast("Muse Code 1.4.6", museHookMinVersion) ||
		!museVersionAtLeast("Muse Code 2.0.0", museHookMinVersion) {
		t.Fatal("museVersionAtLeast")
	}
	if got := tomlString(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("tomlString = %s", got)
	}
}

// Muse's managed hooks file denies its native memory tools on every launch
// that uses it, and the deny command prints the decision Muse documents.
func TestMuseManagedHooksDenyNativeMemory(t *testing.T) {
	dir := t.TempDir()
	mustHookArgv(t, &steerHook{harness: profile.HarnessMuse, dir: dir, command: "dyna hook steer", steer: true}, []string{"muse", "exec", "hi"})
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
			PostToolUse []json.RawMessage `json:"PostToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hooks.PreToolUse) != 1 || doc.Hooks.PreToolUse[0].Matcher != "read_memory|add_memory|edit_memory" || len(doc.Hooks.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("PreToolUse = %s", raw)
	}
	if len(doc.Hooks.PostToolUse) != 1 {
		t.Fatalf("steering hook missing with steering on: %s", raw)
	}
	out, err := exec.Command("sh", "-c", doc.Hooks.PreToolUse[0].Hooks[0].Command).Output()
	if err != nil {
		t.Fatal(err)
	}
	var decision struct {
		Specific struct {
			Event  string `json:"hookEventName"`
			Action string `json:"permissionDecision"`
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &decision); err != nil {
		t.Fatalf("deny output %q: %v", out, err)
	}
	if decision.Specific.Event != "PreToolUse" || decision.Specific.Action != "deny" || decision.Specific.Reason != "Dyna sessions keep no native memory; use your goal, the room or files you own." {
		t.Fatalf("deny output = %s", out)
	}
}

// With steering off, Muse still loads the memory denial but no steering hook.
func TestSteerHooksOffKeepsMuseMemoryDenial(t *testing.T) {
	previous := steerHookSupport
	steerHookSupport = func(string, string) bool { return true }
	t.Cleanup(func() { steerHookSupport = previous })
	t.Setenv(EnvSteerHooks, "off")
	muse, err := newSteerHook(profile.Profile{Harness: profile.HarnessMuse})
	if err != nil || muse == nil {
		t.Fatalf("newSteerHook(muse) = %v, %v; want the memory denial hook", muse, err)
	}
	defer muse.cleanup()
	if muse.usable() {
		t.Fatal("steering uses the hook with steering off")
	}
	mustHookArgv(t, muse, []string{"muse", "exec", "hi"})
	events := museHookEvents(t, muse.dir)
	if _, ok := events["PreToolUse"]; !ok {
		t.Fatal("steering off drops the memory denial")
	}
	if _, ok := events["PostToolUse"]; ok {
		t.Fatal("steering off still installs the PostToolUse hook")
	}
	codex, err := newSteerHook(profile.Profile{Harness: profile.HarnessCodex})
	if err != nil || codex != nil {
		t.Fatalf("newSteerHook(codex) with steering off = %v, %v; want nil", codex, err)
	}
}

// museHookEvents returns the events in the managed hooks file that dir holds.
func museHookEvents(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Hooks
}

// The probe checks the Dyna-managed copy, so a host with no system CLI on PATH
// still gets steering hooks.
func TestSteerHookProbesDynaManagedCopy(t *testing.T) {
	previous := steerHookSupport
	steerHookSupport = probeSteerHookSupport
	t.Cleanup(func() { steerHookSupport = previous })
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvSteerHooks, "")
	exe := filepath.Join(t.TempDir(), "codex")
	if err := fsutil.WriteExecutable(exe, []byte("#!/bin/sh\necho '--dangerously-bypass-hook-trust'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook, err := newSteerHook(profile.Profile{Harness: profile.HarnessCodex, RuntimeExecutable: exe})
	if err != nil || hook == nil {
		t.Fatalf("newSteerHook() = %v, %v; want a hook for the Dyna-managed copy", hook, err)
	}
	hook.cleanup()
}
