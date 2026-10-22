package subagent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	prstate "dyna-agent/internal/pr/state"
)

func writePRLink(t *testing.T, info Info, role, state string, watching bool) {
	t.Helper()
	r := map[string]any{"key": "github.com/owner/repo#17", "number": 17, "url": "https://github.com/owner/repo/pull/17", "roomId": info.RoomID, "state": state, "watching": watching, "authorSession": "another-author"}
	cfg := map[string]any{}
	switch role {
	case "author":
		r["authorSession"] = info.ID
	case "assigned reviewer":
		r["reviewerSession"] = info.ID
	case "reviewer override":
		r["reviewerOverride"] = info.ID
	case "idle reviewer":
		cfg["slots"] = []map[string]any{{"session": info.ID}}
	}
	st := map[string]any{"version": 1, "records": map[string]any{"github.com/owner/repo#17": r}, "reviewers": map[string]any{info.RoomID: cfg}}
	b, _ := json.Marshal(st)
	root := filepath.Join(filepath.Dir(DefaultStore().Root), "prs")
	var stored prstate.State
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal(err)
	}
	if err := prstate.Write(root, stored); err != nil {
		t.Fatal(err)
	}
}

func TestCloseGuardsOpenPRAuthorAndReviewers(t *testing.T) {
	for _, role := range []string{"author", "assigned reviewer", "reviewer override"} {
		t.Run(role, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			writePRLink(t, info, role, "open", true)
			if _, err := e.m.Close(e.ctx, info.ID); err == nil || !strings.HasPrefix(err.Error(), "responsibilities:") || !strings.Contains(err.Error(), "#17 https://github.com/owner/repo/pull/17") || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("close guard for %s: %v", role, err)
			}
			links, err := e.m.TrackedPRs(info.ID)
			if err != nil || len(links) != 1 || !links[0].Active() {
				t.Fatalf("session PR links: %+v %v", links, err)
			}
			if closed, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil || closed.Status != StatusClosed {
				t.Fatalf("force close: %+v %v", closed, err)
			}
		})
	}
	t.Run("idle reviewer", func(t *testing.T) {
		e := newTestEnv(t)
		info := completedFixtureSession(t, e)
		writePRLink(t, info, "idle reviewer", "open", true)
		links, err := e.m.TrackedPRs(info.ID)
		if err != nil || len(links) != 0 {
			t.Fatalf("unattached reviewer has PR links: %+v %v", links, err)
		}
		if closed, err := e.m.Close(e.ctx, info.ID); err != nil || closed.Status != StatusClosed {
			t.Fatalf("idle reviewer close: %+v %v", closed, err)
		}
	})
}

func TestGuardedCloseDoesNotStopTheActiveTurn(t *testing.T) {
	for _, role := range []string{"author", "assigned reviewer", "reviewer override"} {
		t.Run(role, func(t *testing.T) {
			e := newTestEnv(t)
			started := make(chan struct{}, 1)
			resume := make(chan struct{})
			var resumeOnce sync.Once
			t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }) })
			e.m.checkpoint = func(stage string) {
				if stage != "turn-started" {
					return
				}
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-resume:
				case <-e.ctx.Done():
				}
			}
			info, _ := e.start("RESPOND: ready")
			select {
			case <-started:
			case <-time.After(30 * time.Second):
				t.Fatal("mock turn did not start")
			}
			writePRLink(t, info, role, "open", true)
			if _, err := e.m.Close(e.ctx, info.ID); err == nil {
				t.Fatalf("closed a running %s", role)
			}
			current, err := e.m.Store.Resolve(info.ID)
			if err != nil || current.Status != StatusRunning {
				t.Fatalf("guard stopped the turn: %+v %v", current, err)
			}
			turn, err := e.m.Store.ReadTurn(info.ID, current.CurrentTurn)
			if err != nil || turn.StopAsked {
				t.Fatalf("guard asked the turn to stop: %+v %v", turn, err)
			}
			resumeOnce.Do(func() { close(resume) })
			e.wait(info.ID, 1)
			if _, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNormalCloseAfterPRMergeCloseOrUntrack(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		watching    bool
	}{{"merged", "merged", false}, {"closed", "closed", false}, {"untracked", "open", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			writePRLink(t, info, "author", tc.state, tc.watching)
			if closed, err := e.m.Close(e.ctx, info.ID); err != nil || closed.Status != StatusClosed {
				t.Fatalf("normal close after %s: %+v %v", tc.name, closed, err)
			}
		})
	}
}
