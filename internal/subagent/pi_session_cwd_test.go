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
// path. The file path and the session id are one registry key, so the resume
// reuses the live Pi process instead of starting another one.
func TestPiResumeReusesTheSessionProcess(t *testing.T) {
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
	if spawns := strings.Count(string(log), "SPAWN "); spawns != 1 {
		t.Fatalf("pi spawned %d processes, want one reused process:\n%s", spawns, log)
	}
}

// A forked session survives a model fallback. When the provider refuses the
// first forked request before any output, the next model resumes the fork
// instead of starting an empty conversation.
func TestPiForkKeepsHistoryAcrossAModelFallback(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	p := piTestProfile("pi-old", "a", path, logPath)
	p.Models = []string{"a", "b"}
	p.Env["FAKE_PI_FAIL_FORK_QUOTA"] = "1"
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
	if second.Status != TurnOK || second.Model != "b" {
		t.Fatalf("fallback turn = %+v", second)
	}
	current, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HarnessSession == first.HarnessSession || current.HarnessSession == "" {
		t.Fatalf("fallback did not keep the fork id: %q", current.HarnessSession)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var afterFork, resumed, fresh bool
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.HasPrefix(line, "SPAWN ") {
			continue
		}
		if strings.Contains(line, "--fork ") {
			afterFork = true
			continue
		}
		if !afterFork {
			continue
		}
		if strings.Contains(line, "--model b") && strings.Contains(line, "--session ") && !strings.Contains(line, "--session-id ") {
			resumed = true
		}
		if strings.Contains(line, "--model b") && strings.Contains(line, "--session-id ") {
			fresh = true
		}
	}
	if !resumed || fresh {
		t.Fatalf("fallback did not resume the forked session:\n%s", log)
	}
}
