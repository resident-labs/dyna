package ghclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// forgetAccountMemory makes this process look like a fresh engine start.
func forgetAccountMemory() {
	accounts.Lock()
	accounts.choices = map[string]choice{}
	accounts.flights = map[string]*accountFlight{}
	accounts.Unlock()
}

// probeCount reports how many access probes the fake gh recorded.
func probeCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// An engine restart empties the memory cache, so the room's proved account must
// come from dyna.db. A turn that probes again spends quota for nothing.
func TestSessionKeepsTheRoomAccountAcrossAnEngineRestart(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadOnly); err != nil {
		t.Fatal(err)
	}
	probes := filepath.Join(t.TempDir(), "probes")
	t.Setenv("GH_PROBE_LOG", probes)
	installFakeGH(t, `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo probe >> "$GH_PROBE_LOG";;
esac
`)
	session, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if session.Env["GH_TOKEN"] != "" || session.Notice != "" {
		t.Fatalf("first turn host=%q notice=%q, want the stored account inside Dyna", session.Env["GH_HOST"], session.Notice)
	}
	if got := probeCount(t, probes); got != 1 {
		t.Fatalf("first turn probed %d times, want 1", got)
	}

	forgetAccountMemory()
	again, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if got := probeCount(t, probes); got != 1 {
		t.Fatalf("the turn after an engine restart probed GitHub %d times, want none", got-1)
	}
	if again.Env["GH_TOKEN"] != "" || again.Notice != "" {
		t.Fatalf("turn after restart host=%q notice=%q, want the stored account inside Dyna", again.Env["GH_HOST"], again.Notice)
	}
}

// GitHub refusing requests must not stop a turn: the turn starts without GitHub
// access and the agent gets one line naming the reset time.
func TestSessionStartsWithoutGitHubWhenTheProbeIsRateLimited(t *testing.T) {
	records := setupRooms(t, "work/service")
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	probes := filepath.Join(t.TempDir(), "probes")
	t.Setenv("GH_PROBE_LOG", probes)
	installFakeGH(t, fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo probe >> "$GH_PROBE_LOG"; printf 'HTTP/2.0 403 Forbidden\nX-RateLimit-Remaining: 0\nX-RateLimit-Reset: %d\n\n{"message":"API rate limit exceeded"}\n';;
esac
`, reset.Unix()))
	session, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", withoutTokens(os.Environ()))
	if err != nil {
		t.Fatalf("a rate limit failed the turn: %v", err)
	}
	if session.Env["GH_TOKEN"] != "" || session.Env["GH_HOST"] != "github.com" {
		t.Fatalf("rate-limited session host=%q token set=%v, want no token", session.Env["GH_HOST"], session.Env["GH_TOKEN"] != "")
	}
	want := "GitHub is rate limited until " + reset.UTC().Format(time.RFC3339) + ", so GitHub commands may fail this turn"
	if !strings.Contains(session.Notice, want) {
		t.Fatalf("notice = %q, want it to contain %q", session.Notice, want)
	}

	// The next turn in the same window must spend no request at all.
	served, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", withoutTokens(os.Environ()))
	if err != nil {
		t.Fatalf("the second turn failed: %v", err)
	}
	if got := probeCount(t, probes); got != 1 {
		t.Fatalf("second turn probed GitHub again: %d probes, want 1 from the first turn", got)
	}
	if !strings.Contains(served.Notice, want) {
		t.Fatalf("second turn notice = %q", served.Notice)
	}
}
