package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestDeliveriesOpenPlans(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	queries := []string{
		dispatchHasInputs, dispatchInputs, checkinFacts,
		`SELECT s.id FROM sessions s WHERE ` + quiet,
	}
	for i, q := range queries {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			// Supply one value per distinct numbered or anonymous placeholder.
			var args []any
			switch i {
			case 0:
				args = []any{"session", "s", false}
			case 1:
				args = []any{"session", "s", "", false}
			case 2:
				args = []any{"r"}
			case 3:
				args = []any{0}
			}
			rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+q, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(plan, "\n"), "USING INDEX deliveries_open") {
				t.Fatalf("missing open index: %v", plan)
			}
		})
	}
}

// A replay must retain the original read inputs of the active turn, but not
// read steers or originals belonging to older turns. UNION ALL is disjoint.
func TestDispatchOpenReplayEquivalence(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, state := range []string{"pending", "taken", "read", "read", "read"} {
		seq := i + 1
		if _, err := s.writer.Exec(`INSERT INTO events(seq,id,at,type,from_kind,from_id,body) VALUES(?,?,0,'test','engine','',x'7b7d')`, seq, fmt.Sprint(seq)); err != nil {
			t.Fatal(err)
		}
		turn := "active"
		var queue any
		if i == 3 {
			queue = "steer"
		}
		if i == 4 {
			turn = "old"
		}
		if _, err := s.writer.Exec(`INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn,queue) VALUES('session','s',?,'session','none','','','','',1,?,?,?)`, seq, state, turn, queue); err != nil {
			t.Fatal(err)
		}
	}
	old := dispatchProjection + `WHERE d.to_kind=?1 AND d.to_id=?2 AND (d.state IN ('pending','taken') OR (d.state='read' AND d.turn=?3 AND d.queue IS NULL AND ?4)) ORDER BY d.seq`
	for _, session := range []string{"s", "empty"} {
		for _, replay := range []bool{false, true} {
			var results []string
			for _, q := range []string{old, dispatchInputs} {
				rows, err := s.Read().Query(q, "session", session, "active", replay)
				if err != nil {
					t.Fatal(err)
				}
				var seqs []int
				for rows.Next() {
					var seq int
					var rest [14]any
					dest := []any{&seq}
					for i := range rest {
						dest = append(dest, &rest[i])
					}
					if err := rows.Scan(dest...); err != nil {
						t.Fatal(err)
					}
					seqs = append(seqs, seq)
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				results = append(results, fmt.Sprint(seqs))
			}
			if results[0] != results[1] {
				t.Fatalf("session %s replay %v: %v", session, replay, results)
			}
		}
	}
}

// Inspect the actual SQL passed by the callers, rather than duplicating their
// predicates in fixtures. Constants used to compose these queries are explicit.
func TestOpenCallerPlans(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	constants := map[string]string{"checkinFacts": checkinFacts, "quiet": quiet, "closable": closable, "boxUpdateNotice": boxUpdateNotice, "jobRunningNotice": jobRunningNotice}
	var value func(ast.Expr) (string, bool)
	value = func(e ast.Expr) (string, bool) {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				s, err := strconv.Unquote(v.Value)
				return s, err == nil
			}
		case *ast.Ident:
			s, ok := constants[v.Name]
			return s, ok
		case *ast.BinaryExpr:
			a, ok := value(v.X)
			b, ok2 := value(v.Y)
			return a + b, ok && ok2 && v.Op == token.ADD
		}
		return "", false
	}
	for _, name := range []string{"checkins.go", "dormancy.go", "model_retry.go", "input_batch.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "QueryContext" && sel.Sel.Name != "QueryRowContext" && sel.Sel.Name != "ExecContext") {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			q, ok := value(call.Args[1])
			if !ok || !strings.Contains(q, "deliveries") {
				return true
			}
			count++
			// Numbered parameters can be reused; anonymous parameters cannot.
			num, anon := 0, 0
			for i := 0; i < len(q); i++ {
				if q[i] != '?' {
					continue
				}
				j := i + 1
				for j < len(q) && q[j] >= '0' && q[j] <= '9' {
					j++
				}
				if j == i+1 {
					anon++
				} else {
					v, _ := strconv.Atoi(q[i+1 : j])
					if v > num {
						num = v
					}
				}
			}
			if anon > num {
				num = anon
			}
			args := make([]any, num)
			for i := range args {
				args[i] = 0
			}
			rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+q, args...)
			if err != nil {
				t.Errorf("%s: %v\n%s", name, err, q)
				return true
			}
			defer rows.Close()
			used := false
			for rows.Next() {
				var a, b, c int
				var detail string
				if err := rows.Scan(&a, &b, &c, &detail); err != nil {
					t.Fatal(err)
				}
				used = used || strings.Contains(detail, "USING INDEX deliveries_open")
				if strings.Contains(detail, "sqlite_autoindex_deliveries_1") && !strings.Contains(detail, "seq=?") {
					t.Errorf("%s scans recipient history: %s", name, detail)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !used {
				t.Errorf("%s missing open index: %s", name, q)
			}
			return true
		})
		if count == 0 {
			t.Fatalf("no SQL checked in %s", name)
		}
	}
}

func TestDeliveriesOpenUpgrade(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`DROP INDEX deliveries_open; ALTER TABLE deliveries DROP COLUMN taken_at; ALTER TABLE turns DROP COLUMN model_sent_at; DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item; ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE sessions DROP COLUMN advisor; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='100' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sql string
	if err := s.Read().QueryRow(`SELECT sql FROM sqlite_master WHERE name='deliveries_open'`).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	if sql != steps[100][0] {
		t.Fatalf("migrated index: %s", sql)
	}
}
