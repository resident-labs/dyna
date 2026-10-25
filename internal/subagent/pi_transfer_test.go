package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if strings.HasPrefix(line, "SPAWN ") && strings.Contains(line, "--session "+first.HarnessSession) &&
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
