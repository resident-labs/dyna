package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func responsibilityStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0),('s','r','s','root','p','open','',0),('other','r','other','root','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b','s','','agent','ready','','',0,0,'run');`); err != nil {
		t.Fatal(err)
	}
	return s
}

func turnEnded(t *testing.T, s *Store, session string, jobs ...string) {
	t.Helper()
	body, err := json.Marshal(contract.TurnEnded{Session: contract.SessionID(session), Turn: "t", Jobs: func() []contract.ShellJobID {
		ids := make([]contract.ShellJobID, len(jobs))
		for i, job := range jobs {
			ids[i] = contract.ShellJobID(job)
		}
		return ids
	}()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`INSERT INTO events(id,room,at,from_kind,from_id,type,body) VALUES(?,'r',0,'session',?,'turn.ended',?)`, fmt.Sprintf("e%d", len(jobs)), session, body); err != nil {
		t.Fatal(err)
	}
}

func TestEachKindAppearsAndClears(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, add, clear string
		want             contract.Responsibility
	}{
		{"turn", addTurn, "UPDATE turns SET ended_at=1", contract.Responsibility{Kind: contract.ResponsibilityTurn, Group: contract.ResponsibilityNeedsBox, ID: "t"}},
		{"goal", addGoal, "UPDATE goals SET state='done'", contract.Responsibility{Kind: contract.ResponsibilityGoal, Group: contract.ResponsibilityOwesWork, ID: "g1"}},
		{"pr", addPR, "UPDATE pr_owners SET state='merged'", contract.Responsibility{Kind: contract.ResponsibilityPR, Group: contract.ResponsibilityOwesWork, ID: "repo#1"}},
		{"issue", "INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('repo',2,'s','u','t','a','open')", "UPDATE issue_owners SET state='closed'", contract.Responsibility{Kind: contract.ResponsibilityIssue, Group: contract.ResponsibilityOwesWork, ID: "repo#2"}},
		{"question", "INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q','r','s','why','pending',0)", "UPDATE questions SET state='answered'", contract.Responsibility{Kind: contract.ResponsibilityQuestion, Group: contract.ResponsibilityOwesWork, ID: "q"}},
		{"blocked question", "INSERT INTO questions(id,room,session,text,state,asked_at,blocked) VALUES('q','r','s','why','pending',0,1)", "UPDATE questions SET state='withdrawn'", contract.Responsibility{Kind: contract.ResponsibilityQuestion, Group: contract.ResponsibilityOwesWork, ID: "q", Blocked: true}},
		{"child", addChild, "UPDATE sessions SET closed_at=1 WHERE id='c'", contract.Responsibility{Kind: contract.ResponsibilityChild, Group: contract.ResponsibilityOwesWork, ID: "c"}},
		{"unnamed job", "UPDATE sessions SET shell_jobs=1 WHERE id='s'", "UPDATE sessions SET shell_jobs=0", contract.Responsibility{Kind: contract.ResponsibilityShellJob, Group: contract.ResponsibilityNeedsBox}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := responsibilityStore(t)
			if _, err := s.writer.Exec(tc.add); err != nil {
				t.Fatal(err)
			}
			held, err := s.Responsibilities(ctx, "s")
			if err != nil || !reflect.DeepEqual([]contract.Responsibility(held), []contract.Responsibility{tc.want}) {
				t.Fatalf("held %+v: %v", held, err)
			}
			if _, err := s.writer.Exec(tc.clear); err != nil {
				t.Fatal(err)
			}
			if held, err := s.Responsibilities(ctx, "s"); err != nil || len(held) != 0 {
				t.Fatalf("still held %+v: %v", held, err)
			}
		})
	}
}

func TestShellJobsAreNamedAndLeaveWhenTheyEnd(t *testing.T) {
	ctx := context.Background()
	s := responsibilityStore(t)
	turnEnded(t, s, "s", "job_a", "job_b")
	if _, err := s.writer.Exec("UPDATE sessions SET shell_jobs=2 WHERE id='s'"); err != nil {
		t.Fatal(err)
	}
	held, err := s.Responsibilities(ctx, "s")
	if err != nil || len(held) != 2 || held[0].ID != "job_a" || held[1].ID != "job_b" || !held.NeedsBox() {
		t.Fatalf("held %+v: %v", held, err)
	}
	if _, err := s.writer.Exec("INSERT INTO shell_jobs(session,job) VALUES('s','job_a')"); err != nil {
		t.Fatal(err)
	}
	held, err = s.Responsibilities(ctx, "s")
	if err != nil || len(held) != 1 || held[0].ID != "job_b" {
		t.Fatalf("held %+v: %v", held, err)
	}
	// A new box agent has no jobs, whatever the last turn named.
	if err := s.ForgetShellJobs(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if held, err = s.Responsibilities(ctx, "s"); err != nil || len(held) != 0 {
		t.Fatalf("held %+v: %v", held, err)
	}
}

func TestATerminalNeedsTheBox(t *testing.T) {
	ctx := context.Background()
	s := responsibilityStore(t)
	s.SetTerminals(func(id contract.SessionID) bool { return id == "s" })
	held, err := s.Responsibilities(ctx, "s")
	if err != nil || len(held) != 1 || held[0].Kind != contract.ResponsibilityTerminal || !held.NeedsBox() {
		t.Fatalf("held %+v: %v", held, err)
	}
	if held, err = s.Responsibilities(ctx, "other"); err != nil || len(held) != 0 {
		t.Fatalf("held %+v: %v", held, err)
	}
}

func TestAWorkflowRunNeedsTheBoxWhileItsTurnIsOpen(t *testing.T) {
	ctx := context.Background()
	s := responsibilityStore(t)
	if _, err := s.writer.Exec(addTurn + `; INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,result,started_at,ended_at) VALUES('wf','r','s','n','','','','','','','','running','',0,0)`); err != nil {
		t.Fatal(err)
	}
	held, err := s.Responsibilities(ctx, "s")
	if err != nil || held.Count(contract.ResponsibilityWorkflow) != 1 {
		t.Fatalf("held %+v: %v", held, err)
	}
	if _, err := s.writer.Exec("UPDATE turns SET ended_at=1"); err != nil {
		t.Fatal(err)
	}
	if held, err = s.Responsibilities(ctx, "s"); err != nil || held.NeedsBox() {
		t.Fatalf("a failed crew's run holds its box: %+v: %v", held, err)
	}
}

type countingQueries struct {
	sqlQuery
	queries int
}

func (c *countingQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.sqlQuery.QueryContext(ctx, query, args...)
}

func TestTheBatchedFormMatchesTheSingleFormInOneQuery(t *testing.T) {
	ctx := context.Background()
	s := responsibilityStore(t)
	if _, err := s.writer.Exec(addTurn + ";" + addGoal + ";" + addPR + ";" + addChild + `;
INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',10,'s','x','y','active',0,0),('r',9,'s','x','y','active',0,0),('r',2,'other','x','y','active',0,0);
INSERT INTO questions(id,room,session,text,state,asked_at,blocked) VALUES('q','r','other','why','pending',0,1);
UPDATE sessions SET shell_jobs=1 WHERE id='other'`); err != nil {
		t.Fatal(err)
	}
	s.SetTerminals(func(id contract.SessionID) bool { return id == "root" })
	ids := []contract.SessionID{"root", "s", "other", "c", "nobody"}
	counter := &countingQueries{sqlQuery: s.Read()}
	batch, err := s.responsibilities(ctx, counter, ids, s.openTerminals(ids))
	if err != nil {
		t.Fatal(err)
	}
	if counter.queries != 1 {
		t.Fatalf("%d queries for %d sessions", counter.queries, len(ids))
	}
	for _, id := range ids {
		single, err := s.Responsibilities(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(single, batch[id]) {
			t.Fatalf("%s: single %+v, batch %+v", id, single, batch[id])
		}
	}
	var kinds []string
	for _, item := range batch["s"] {
		kinds = append(kinds, string(item.Kind)+" "+item.ID)
	}
	want := []string{"turn t", "goal g1", "goal g9", "goal g10", "pr repo#1", "child c"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("order %v", kinds)
	}
	if !batch["other"].Blocked() || batch["s"].Blocked() {
		t.Fatalf("blocked %+v", batch)
	}
}

// Each reader's answer follows the list, kind by kind.
func TestBoxStopAndTheClosesAgreeWithTheList(t *testing.T) {
	ctx := context.Background()
	ended := "; INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('old','s',0,1,'completed','m',0,0,0,0)"
	cutoff := time.UnixMilli(1000000)
	for _, tc := range []struct {
		name, change        string
		stops, week, evicts bool
	}{
		{"nothing", "", true, true, true},
		{"goal", addGoal, true, false, true},
		{"pr", addPR, true, false, true},
		{"issue", "INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('repo',2,'s','u','t','a','open')", true, true, true},
		{"question", "INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q','r','s','why','pending',0)", true, false, false},
		{"child", addChild, true, false, false},
		{"job", "UPDATE sessions SET shell_jobs=1 WHERE id='s'", false, false, false},
		{"turn", strings.Replace(addTurn, "'t','s',0", "'t','s',5", 1), false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := responsibilityStore(t)
			if _, err := s.writer.Exec("INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,1,0)" + ended); err != nil {
				t.Fatal(err)
			}
			if tc.change != "" {
				if _, err := s.writer.Exec(tc.change); err != nil {
					t.Fatal(err)
				}
			}
			held, err := s.Responsibilities(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			if held.NeedsBox() == tc.stops {
				t.Fatalf("list %+v, stops %v", held, tc.stops)
			}
			stopped, err := s.StopQuiet(ctx, []contract.SessionID{"s"}, cutoff)
			if err != nil || (len(stopped) == 1) != tc.stops {
				t.Fatalf("stopped %v: %v", stopped, err)
			}
			if _, err := s.writer.Exec("UPDATE boxes SET state='stopped',want='stop',host='h' WHERE session='s'; UPDATE hosts SET disk_floor=10,disk_low_since=1"); err != nil {
				t.Fatal(err)
			}
			week, err := s.DormantCloseCandidates(ctx, cutoff)
			if err != nil || (len(week) == 1) != tc.week {
				t.Fatalf("7-day %v: %v", week, err)
			}
			evict, err := s.DiskEvictCandidates(ctx, time.UnixMilli(2))
			if err != nil || (len(evict) == 1) != tc.evicts {
				t.Fatalf("eviction %v: %v", evict, err)
			}
		})
	}
}

// The reconciler runs this over every ready box on each pass, so no branch may scan a table.
func TestTheListWalksIndexes(t *testing.T) {
	s := responsibilityStore(t)
	rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+responsibilityQuery, `["s","other"]`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	scan := regexp.MustCompile(`SCAN (t|g|p|i|q|c|r|s|x)\b`)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if scan.MatchString(detail) {
			t.Fatalf("plan: %s", detail)
		}
	}
}

func TestEveryKindHasARule(t *testing.T) {
	seen := map[contract.ResponsibilityKind]bool{}
	for _, rule := range responsibilityRules {
		if seen[rule.kind] || rule.group == "" {
			t.Fatalf("rule %+v", rule)
		}
		seen[rule.kind] = true
	}
	for _, kind := range []contract.ResponsibilityKind{contract.ResponsibilityTurn, contract.ResponsibilityShellJob, contract.ResponsibilityTerminal, contract.ResponsibilityWorkflow, contract.ResponsibilityGoal, contract.ResponsibilityPR, contract.ResponsibilityIssue, contract.ResponsibilityQuestion, contract.ResponsibilityChild} {
		if !seen[kind] {
			t.Fatalf("no rule for %s", kind)
		}
	}
}

// The list is what the 7-day close and eviction read, so the sweep never gives up on what sits behind a held session.
func TestHeldSessionsDoNotFillTheCandidateLimit(t *testing.T) {
	ctx := context.Background()
	s := responsibilityStore(t)
	for i := range 300 {
		if _, err := s.writer.Exec(fmt.Sprintf(`INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('h%d','r','h%d','root','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('bh%d','h%d','','agent','stopped','','',0,1,'stop');
INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',%d,'h%d','x','y','active',0,0)`, i, i, i, i, 100+i, i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='stopped',want='stop',updated_at=2 WHERE session='s'"); err != nil {
		t.Fatal(err)
	}
	ids, err := s.DormantCloseCandidates(ctx, time.UnixMilli(1000))
	if err != nil || len(ids) != 1 || ids[0] != "s" {
		t.Fatalf("candidates %v: %v", ids, err)
	}
}
