package worktrees

import (
	"context"
	"os"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
)

func TestIncompleteRunKeepsUnlistedTree(t *testing.T) {
	tree := inferenceTestStore(t)
	t.Setenv("DYNA_RUN_ID", "wf_incomplete")
	t.Setenv(runstore.SessionEnv, "")
	run, err := runstore.Create("ownership", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET body=json_set(body,'$.status','ok','$.endedAt','2026-01-01T00:00:00Z') WHERE id=?`, run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO run_directories(run_id,body,complete) VALUES(?,'[]',0) ON CONFLICT(run_id) DO UPDATE SET body='[]',complete=0`, run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	history := &runHistory{}
	for _, removeArtifacts := range []bool{false, true} {
		if removeArtifacts {
			if err := os.RemoveAll(run.Dir); err != nil {
				t.Fatal(err)
			}
		}
		got, err := inferOwners(context.Background(), []string{tree}, history)
		if err != nil || len(got[tree].Runs) != 1 || got[tree].Runs[0].Closed {
			t.Fatalf("incomplete run did not protect unlisted tree: %#v, %v", got[tree], err)
		}
		if history.read[run.Meta.ID] {
			t.Fatal("cached incomplete directory evidence")
		}
	}
}
