package subagent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A Pi session whose recorded directory disappeared cannot be opened from the
// turn's directory: Pi asks on stdin whether to fork, and a headless turn
// aborts. Dyna forks it with a new id instead, and the next turn keeps the id.
func TestPiResumeForksWhenTheSessionDirectoryIsGone(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	p := piTestProfile("pi-old", "old/provider-model", path, logPath)
	p.Env["FAKE_PI_FAIL_CROSS_PROJECT"] = "1"
	e := newTestEnv(t, p)
	info, _ := e.start("RESPOND: first turn", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	writePiSession(t, first.HarnessSession, "/does/not/exist/dyna-117")
	if _, err := e.m.Assign(context.Background(), info.ID, "RESPOND: second turn", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	info, _ = e.wait(info.ID, 2)
	second, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != TurnOK {
		t.Fatalf("turn failed: %+v", second)
	}
	current, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HarnessSession == first.HarnessSession || current.HarnessSession == "" {
		t.Fatalf("fork did not record a new session id: %q", current.HarnessSession)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var forked bool
	for _, line := range strings.Split(string(log), "\n") {
		if strings.HasPrefix(line, "SPAWN ") && strings.Contains(line, "--fork ") &&
			strings.Contains(line, "--session-id "+current.HarnessSession) {
			forked = true
		}
	}
	if !forked {
		t.Fatalf("pi was not asked to fork the session:\n%s", log)
	}
}

// While the session's own directory exists, a resume opens the session file by
// path. Pi keeps using that directory and never asks whether to fork.
func TestPiResumeOpensTheSessionByPath(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	p := piTestProfile("pi-old", "old/provider-model", path, logPath)
	e := newTestEnv(t, p)
	info, _ := e.start("RESPOND: first turn", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	writePiSession(t, first.HarnessSession, info.Dir)
	if _, err := e.m.Assign(context.Background(), info.ID, "RESPOND: second turn", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	info, _ = e.wait(info.ID, 2)
	second, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != TurnOK {
		t.Fatalf("turn failed: %+v", second)
	}
	current, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HarnessSession != first.HarnessSession {
		t.Fatalf("resume changed the session id: %q", current.HarnessSession)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var byPath, forked bool
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.HasPrefix(line, "SPAWN ") {
			continue
		}
		if strings.Contains(line, "--session ") && strings.Contains(line, "_"+first.HarnessSession+".jsonl") {
			byPath = true
		}
		if strings.Contains(line, "--fork ") {
			forked = true
		}
	}
	if !byPath || forked {
		t.Fatalf("pi did not open the session file by path: %s", log)
	}
}
