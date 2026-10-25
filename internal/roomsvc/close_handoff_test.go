package roomsvc

import (
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/subagent"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestCloseHandsAuthorshipAndGoalsToAncestor(t *testing.T) {
	for _, ancestry := range []string{"parent", "closed parent", "root", "pruned parent"} {
		t.Run(ancestry, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("alpha")
			parent := e.start(rec, "lead", nil)
			author := e.start(rec, "author", &parent)
			if ancestry == "closed parent" {
				middle := e.start(rec, "middle", &parent)
				if _, err := e.mgr.Store.UpdateMeta(middle.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
					t.Fatal(err)
				}
				if _, err := e.mgr.Store.UpdateMeta(author.ID, func(m *subagent.Meta) { m.ParentSession = middle.ID }); err != nil {
					t.Fatal(err)
				}
			}
			if ancestry == "root" {
				if _, err := e.mgr.Store.UpdateMeta(parent.ID, func(m *subagent.Meta) { m.Role = subagent.RoleRoot; m.Name = "root" }); err != nil {
					t.Fatal(err)
				}
				if _, err := e.mgr.Store.UpdateMeta(author.ID, func(m *subagent.Meta) { m.ParentSession = ""; m.Depth = 1 }); err != nil {
					t.Fatal(err)
				}
				parent.Name = "root"
			}
			if ancestry == "pruned parent" {
				middle := e.start(rec, "middle", &parent)
				if _, err := e.mgr.Store.UpdateMeta(author.ID, func(m *subagent.Meta) { m.ParentSession = middle.ID; m.Closed = time.Now() }); err != nil {
					t.Fatal(err)
				}
				if _, err := e.mgr.Store.UpdateMeta(middle.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
					t.Fatal(err)
				}
				if err := sessionlife.Delete(e.mgr.Store.Root, middle.ID); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(e.mgr.Store.Root, middle.ID)); err != nil {
					t.Fatal(err)
				}
				if _, err := e.mgr.Store.UpdateMeta(parent.ID, func(m *subagent.Meta) { m.Role = subagent.RoleRoot; m.Name = "root" }); err != nil {
					t.Fatal(err)
				}
				parent.Name = "root"
				if _, err := e.mgr.Reopen(e.ctx, author.ID); err != nil {
					t.Fatal(err)
				}
			}
			const key = "github.com/owner/repo#17"
			prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
				key: {Key: key, Number: 17, RoomID: rec.ID, Author: author.ID, State: "open", Watching: true, Notices: []watch.Notice{{ID: "pending", Target: author.ID, Text: "Continue this work"}}},
			}})
			g, err := goals.DefaultStore().Add(rec.ID, author.ID, goals.Input{Text: "Keep this work", Holders: []string{author.ID}})
			if err != nil {
				t.Fatal(err)
			}
			closed, err := e.svc.CloseSession(e.ctx, rec.ID, author.ID)
			if ancestry == "root" || ancestry == "pruned parent" {
				// A close only reaches root: the goal must be resolved first, and a
				// forced close does not hand it to the owner either.
				if err == nil || !strings.Contains(err.Error(), "dyna goal done "+g.ID) {
					t.Fatalf("close with root above = %+v, %v; want the goal refusal", closed, err)
				}
				if _, err = e.svc.ForceCloseSession(e.ctx, rec.ID, author.ID); err == nil || !strings.Contains(err.Error(), "dyna ask") {
					t.Fatalf("forced close = %v; want the goal refusal", err)
				}
				// Root asks the owner, then finishes the goal and closes.
				if _, err = e.svc.goalService().Store.Finish(rec.ID, g.ID, "owner", "Fixture completed", false); err != nil {
					t.Fatal(err)
				}
				if closed, err = e.svc.CloseSession(e.ctx, rec.ID, author.ID); err != nil || !closed.Closed {
					t.Fatalf("close after the ownered goal resolved = %+v, %v", closed, err)
				}
			} else if err != nil || !closed.Closed {
				t.Fatalf("close = %+v, %v", closed, err)
			}
			r, err := e.svc.PRs.Watcher().Store.Get(key)
			if err != nil || r.Author != parent.ID || len(r.FormerAuthors) != 1 || r.FormerAuthors[0] != author.ID {
				t.Fatalf("author handoff = %+v, %v", r, err)
			}
			got, err := goals.DefaultStore().Get(rec.ID, g.ID)
			if ancestry == "root" || ancestry == "pruned parent" {
				// Root holds no goals, and the finished goal waits for nobody.
				if err != nil || got.Open() || len(got.Holders) != 0 {
					t.Fatalf("goal after the close = %+v, %v", got, err)
				}
			} else if err != nil || !got.Open() || !got.HeldBy(parent.ID) || got.HeldBy(author.ID) {
				t.Fatalf("goal handoff = %+v, %v", got, err)
			}
			// The result must tell the app where the work went.
			if !movedPR(closed.CloseSummary, 17, parent.Address()) {
				t.Fatalf("missing the PR handoff in the close result: %+v", closed.CloseSummary)
			}

		})
	}
}

// No runner is started: cancel finalizes this run from its stored metadata.
func TestCloseCancelsWorkflowFromSQLite(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	author := e.start(rec, "author", nil)
	if _, err := e.svc.goalService().Store.Finish(rec.ID, author.Goal, "owner", "Fixture completed", false); err != nil {
		t.Fatal(err)
	}
	const runID = "wf_close-handoff"
	dir := filepath.Join(runstore.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(runstore.Meta{ID: runID, Session: author.ID, ParentSession: author.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Import a runner fixture with no live process into the canonical store.
	if _, err := runstore.ReadMeta(runID); err != nil {
		t.Fatal(err)
	}
	// The workflow reader must not depend on the rollback file.
	if err := os.Remove(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}
	closed, err := e.svc.CloseSession(e.ctx, rec.ID, author.ID)
	if err != nil || !closed.Closed {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	got, err := runstore.ReadMeta(runID)
	if err != nil || got.Status != "canceled" {
		t.Fatalf("run = %+v, %v", got, err)
	}
	if !canceledWorkflow(closed.CloseSummary, runID) {
		t.Fatalf("missing the canceled run in the close result: %+v", closed.CloseSummary)
	}
}

// movedPR reports whether a close gave PR number to the session at to.
func movedPR(outcomes []sessionclose.Outcome, number int, to string) bool {
	return slices.ContainsFunc(outcomes, func(o sessionclose.Outcome) bool {
		return o.Kind == sessionclose.ResponsibilityPR && o.Result == sessionclose.ResultMoved && o.Number == number && o.To == to
	})
}

// canceledWorkflow reports whether a close canceled the run id.
func canceledWorkflow(outcomes []sessionclose.Outcome, id string) bool {
	return slices.ContainsFunc(outcomes, func(o sessionclose.Outcome) bool {
		return o.Kind == sessionclose.ResponsibilityWorkflow && o.Result == sessionclose.ResultCanceled && o.ID == id
	})
}
