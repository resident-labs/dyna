package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

// Count actual driver calls, rather than inferring query count from source. This driver is test-only.
type workflowCountingDriver struct{ calls atomic.Int64 }
type workflowCountingConn struct {
	driver.Conn
	calls *atomic.Int64
}

func (d *workflowCountingDriver) Open(name string) (driver.Conn, error) {
	c, e := (&sqlite.Driver{}).Open(name)
	return &workflowCountingConn{c, &d.calls}, e
}
func (c *workflowCountingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.calls.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

var workflowDriverID atomic.Int64

func TestWorkflowListOneQueryAndLightHeaders(t *testing.T) {
	s, ctx := branchStore(t)
	for i := 0; i < 100; i++ {
		if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES(?,'room',?,'p','ready','',0)", fmt.Sprint("crew", i), fmt.Sprint("crew", i)); err != nil {
			t.Fatal(err)
		}
		_, err := s.writer.ExecContext(ctx, `INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,result,started_at) VALUES(?,'room',?,'sweep','','script','','["p"]','owner/repo','','','done',?,?)`, fmt.Sprint(i), fmt.Sprint("crew", i), strings.Repeat("large result", 1000), i)
		// Runs reference a crew, so create it before inserting the run.
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, branch := range []string{"first", "second"} {
		if _, err := s.writer.ExecContext(ctx, "INSERT INTO session_branches(repo,branch,session,created_at) VALUES('owner/repo',?,'crew99',1)", branch); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 25; i++ {
		if _, err := s.writer.ExecContext(ctx, "INSERT INTO workflow_steps(run,key,parent,kind,name,phase,profile,detail,state,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_at) VALUES('99',?,'','agent',?,'','p','private detail','failed',1,2,3,4,?)", fmt.Sprint(i), fmt.Sprint("failed", i), i); err != nil {
			t.Fatal(err)
		}
	}
	d := &workflowCountingDriver{}
	name := fmt.Sprint("workflow-count-", workflowDriverID.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, filepath.Join(filepath.Dir(s.lock.Name()), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := s.read
	s.read = db
	defer func() { s.read = old; db.Close() }()
	// Execute the previous list path on the same 100 runs to pair actual SQL counts.
	before, err := s.WorkflowRuns(ctx, "room")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range before {
		if _, err := s.WorkflowSummary(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := s.WorkflowCounts(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionBranches(ctx, run.Repo, run.Session); err != nil {
			t.Fatal(err)
		}
	}
	if d.calls.Load() != 301 {
		t.Fatalf("baseline queries %d", d.calls.Load())
	}
	t.Logf("same 100 runs: before %d driver queries", d.calls.Load())
	d.calls.Store(0)
	runs, err := s.WorkflowList(ctx, "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 100 || d.calls.Load() != 1 {
		t.Fatalf("runs %d queries %d", len(runs), d.calls.Load())
	}
	if r := runs[0]; r.Branch != "first" || r.Failed != 25 || r.Steps != 25 || len(r.FailedSteps) != 20 || r.FailedSteps[19] != "failed19" || r.Usage.InputTokens != 25 || r.Usage.OutputTokens != 50 || r.Usage.CacheReadTokens != 75 || r.Usage.CacheWriteTokens != 100 {
		t.Fatalf("totals %+v", r)
	}
	for _, r := range runs {
		if r.Result != "" {
			t.Fatalf("not a lightweight empty header: %+v", r)
		}
	}
}
