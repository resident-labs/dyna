package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
)

// fakePiTurn speaks the Pi rpc protocol for one turn at a time: it answers the
// prompt, reports the session it runs in, then stays alive for the next turn,
// like pi --mode rpc. It never contacts a provider, so no sign-in is needed.
const fakePiTurn = `#!/usr/bin/env python3
import json, os, sys

args = sys.argv[1:]
log = os.environ.get("FAKE_PI_LOG")

def logf(line):
    if log:
        with open(log, "a") as handle:
            handle.write(line + "\n")

logf("SPAWN " + " ".join(args))

if "--version" in args:
    print("fake-pi-1.0")
    sys.exit(0)

# FAKE_PI_COUNT numbers the launches. A failure mode can then hit one launch
# only, and a resume can be refused when it carries --session.
launch = 0
count = os.environ.get("FAKE_PI_COUNT")
if count:
    try:
        with open(count) as handle:
            launch = int(handle.read().strip() or "0")
    except (OSError, ValueError):
        launch = 0
    with open(count, "w") as handle:
        handle.write(str(launch + 1))
resume = any(arg == "--session" for arg in args)
if os.environ.get("FAKE_PI_FAIL_FIRST") == "1" and launch == 0:
    sys.stderr.write("fake pi: first launch failed before a session existed\n")
    sys.exit(2)
if os.environ.get("FAKE_PI_FAIL_RESUME") == "1" and resume:
    sys.stderr.write("fake pi: context_length_exceeded while resuming\n")
    sys.exit(2)
if os.environ.get("FAKE_PI_FAIL_CROSS_PROJECT") == "1" and "--session" in args and "--fork" not in args:
    sys.stderr.write("fake pi: Session found in different project. Fork this session into current directory? [y/N] Aborted.\n")
    sys.exit(2)
if os.environ.get("FAKE_PI_FAIL_FORK_QUOTA") == "1" and "--fork" in args:
    sys.stderr.write("429 quota exhausted\n")
    sys.exit(2)

def emit(record):
    print(json.dumps(record), flush=True)

session = ""
for index, arg in enumerate(args):
    if arg in ("--session", "--session-id") and index + 1 < len(args):
        session = args[index + 1]
if not session:
    session = "fake-pi-session"

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        command = json.loads(line)
    except ValueError:
        continue
    kind = command.get("type")
    rid = command.get("id", "")
    if kind == "prompt":
        emit({"id": rid, "type": "response", "command": "prompt", "success": True,
              "data": {"disposition": "started"}})
        emit({"type": "session", "id": session})
        emit({"type": "message_end", "message": {
            "role": "assistant",
            "content": [{"type": "text", "text": "done: " + command.get("message", "")}],
            "usage": {"input": 10, "output": 5, "cost": {"total": 0.01}}}})
        emit({"type": "agent_settled"})
    elif kind == "abort":
        emit({"id": rid, "type": "response", "command": "abort", "success": True})
    elif kind == "set_auto_retry":
        emit({"id": rid, "type": "response", "command": "set_auto_retry", "success": True})
    elif kind == "compact":
        emit({"id": rid, "type": "response", "command": "compact", "success": True,
              "data": {"summary": "ok"}})
`

// installFakePiTurn puts a fake pi on a private PATH and returns that PATH plus
// the file where the fake appends each launch's arguments and prompts.
func installFakePiTurn(t *testing.T) (path, logPath string) {
	t.Helper()
	t.Cleanup(sessionhost.ResetForTest)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte(fakePiTurn), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "pi.log")
}

func piTestProfile(name, model, path, logPath string) profile.Profile {
	return profile.Profile{
		Name: name, Harness: profile.HarnessPi, Model: model, Taste: 5, Intelligence: 5, Cost: 5,
		Env: map[string]string{
			"PATH":        path + string(os.PathListSeparator) + os.Getenv("PATH"),
			"FAKE_PI_LOG": logPath,
		},
	}
}

// persistFakePiSession writes the session file a real pi keeps once a session
// exists, so a later resume of the id finds it. The fake itself writes none.
func persistFakePiSession(t *testing.T, id string) {
	t.Helper()
	home, err := harnessruntime.NewAccounts().HarnessHome(profile.HarnessPi)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260101-000000_"+id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPiTransferKeepsTheSamePiSession proves a move between two Pi profiles
// resumes the exact Pi session with the target model: no handoff ask, no
// handoff packet and no fresh conversation. Other pairs keep that path, so a
// Pi target that is not preceded by a Pi session still waits for a handoff.
func TestPiTransferKeepsTheSamePiSession(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := piTestProfile("pi-next", "new/provider-model", path, logPath)
	e := newTestEnv(t, old, next)
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	if first.HarnessSession == "" {
		t.Fatal("first turn reported no Pi session")
	}
	persistFakePiSession(t, first.HarnessSession)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "pi-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "stopping" || state.Posted {
		t.Fatalf("Pi transfer did not skip the handoff wait: %+v", state)
	}
	completeTransfer(t, e, state)
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "pi-next" || meta.Model != "new/provider-model" || !meta.Resumable || meta.HarnessSession != first.HarnessSession {
		t.Fatalf("transfer did not keep the Pi session: %+v", meta)
	}
	nextTurn, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if nextTurn.HarnessSession != first.HarnessSession {
		t.Fatalf("replacement turn ran a different session: %+v", nextTurn)
	}
	if strings.Contains(nextTurn.Prompt, "[DYNA HANDOFF PACKET]") || strings.Contains(nextTurn.Prompt, "no native context") {
		t.Fatalf("replacement got a handoff packet: %s", nextTurn.Prompt)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var resumed bool
	for _, line := range strings.Split(string(log), "\n") {
		if strings.HasPrefix(line, "SPAWN ") && strings.Contains(line, "--session ") && strings.Contains(line, first.HarnessSession) &&
			strings.Contains(line, "--model new/provider-model") {
			resumed = true
		}
	}
	if !resumed {
		t.Fatalf("pi did not resume the same session with the new model:\n%s", log)
	}
}

// A Pi target has nothing to resume when the prior profile is not Pi, so the
// transfer must still ask for a handoff and start a fresh conversation.
func TestTransferToPiFromAnotherHarnessKeepsTheHandoffPath(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	next := piTestProfile("pi-next", "new/provider-model", path, logPath)
	e, _ := transferEnv(t)
	store, err := e.m.profiles()
	if err != nil {
		t.Fatal(err)
	}
	store.Profiles = append(store.Profiles, next)
	info, _ := e.start("RESPOND: first")
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "pi-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "waiting" {
		t.Fatalf("non-Pi source skipped the handoff wait: %+v", state)
	}
}

// piFallbackEnv installs the two Pi profiles and the shared launch counter the
// failure modes use. Every Pi turn then runs the same fake.
func piFallbackEnv(t *testing.T, old, next *profile.Profile, path string) *testEnv {
	t.Helper()
	count := filepath.Join(path, "launches")
	for _, p := range []*profile.Profile{old, next} {
		p.Env["FAKE_PI_COUNT"] = count
	}
	e := newTestEnv(t, *old, *next)
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	// The harness gives a failed attempt one same-session nudge. Skip its
	// wall-clock delay; the fake refuses that resume too.
	delay := time.Millisecond
	e.m.RecoveryDelay = &delay
	return e
}

// driveTransfer runs the coordinator until the transfer ends, letting each
// replacement turn finish first.
func driveTransfer(t *testing.T, e *testEnv, state TransferState) {
	t.Helper()
	for range 16 {
		if _, err := e.m.advanceTransfer(e.ctx, state.Session, state.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.TickSessions(e.ctx); err != nil {
			t.Fatal(err)
		}
		current, err := e.m.TransferStatus(state.Session)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == "completed" {
			return
		}
		if current.Phase == "failed" {
			t.Fatalf("transfer failed: %+v", current)
		}
		info, err := e.m.Store.Resolve(state.Session)
		if err != nil {
			t.Fatal(err)
		}
		if info.Status == StatusRunning || info.Status == StatusQueued {
			e.wait(state.Session, 0)
		}
	}
	t.Fatal("transfer did not finish")
}

// assertFreshFallback checks that the native attempt was abandoned for the
// bounded handoff conversation, and that the fresh turn carried the packet.
// wantResume says whether a resume launch was expected first: a session file
// that never existed is refused before any launch.
func assertFreshFallback(t *testing.T, e *testEnv, id string, state TransferState, logPath string, wantResume bool) {
	t.Helper()
	got, err := e.m.TransferStatus(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "completed" || got.Source != "transcript" || !strings.Contains(got.Reason, "could not resume") {
		t.Fatalf("no fresh fallback: %+v", got)
	}
	turn, err := e.m.Store.ReadTurn(id, got.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.Prompt, "[DYNA HANDOFF PACKET]") {
		t.Fatalf("fallback turn got no handoff packet: %s", turn.Prompt)
	}
	meta, err := e.m.Store.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "pi-next" || meta.Model != "new/provider-model" {
		t.Fatalf("fallback did not move the session: %+v", meta)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var resumed, fresh bool
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.HasPrefix(line, "SPAWN ") {
			continue
		}
		if strings.Contains(line, "--session ") {
			resumed = true
		}
		if strings.Contains(line, "--session-id ") && strings.Contains(line, "--model new/provider-model") {
			fresh = true
		}
	}
	if resumed != wantResume || !fresh {
		t.Fatalf("fallback launches (resume=%v, want %v; fresh=%v):\n%s", resumed, wantResume, fresh, log)
	}
}

// A first Pi launch can fail before Pi writes any session file, while Dyna has
// already recorded the preassigned id. The turn recovers with a fresh
// session (never a nudge at the missing session), but no session file ever
// exists, so a later transfer cannot resume natively and must recover with
// the fresh handoff conversation instead of repeating the same failure.
func TestPiTransferFallsBackWhenNoSessionExists(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := piTestProfile("pi-next", "new/provider-model", path, logPath)
	for _, p := range []*profile.Profile{&old, &next} {
		p.Env["FAKE_PI_FAIL_FIRST"] = "1"
		p.Env["FAKE_PI_FAIL_RESUME"] = "1"
	}
	e := piFallbackEnv(t, &old, &next, path)
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	if first.Status != TurnOK {
		t.Fatalf("first launch did not recover with a fresh session: %+v", first)
	}
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "pi-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "stopping" {
		t.Fatalf("Pi transfer did not skip the handoff wait: %+v", state)
	}
	// No session file exists, so the resume is refused before any launch and
	// the fallback starts at once.
	driveTransfer(t, e, state)
	assertFreshFallback(t, e, info.ID, state, logPath, false)
}

// A history that no longer fits the target model can fail both Pi's compaction
// and the continuation. The bounded handoff conversation is the fallback.
func TestPiTransferFallsBackWhenTheSessionCannotFit(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := piTestProfile("pi-next", "new/provider-model", path, logPath)
	for _, p := range []*profile.Profile{&old, &next} {
		p.Env["FAKE_PI_FAIL_RESUME"] = "1"
	}
	e := piFallbackEnv(t, &old, &next, path)
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	if first.Status != TurnOK || first.HarnessSession == "" {
		t.Fatalf("first turn did not produce a Pi session: %+v", first)
	}
	persistFakePiSession(t, first.HarnessSession)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "pi-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	driveTransfer(t, e, state)
	assertFreshFallback(t, e, info.ID, state, logPath, true)
}
