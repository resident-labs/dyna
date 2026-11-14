package store

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Extract the actual production SQL so a caller regression cannot leave the plan test green.
func historyQueries(t testing.TB) []string {
	t.Helper()
	var queries []string
	for _, file := range []string{"domain.go", "replies.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			q, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(q, "UPDATE deliveries") && strings.Contains(q, "turn=? AND state='taken'") && !strings.Contains(q, "seq=?") || strings.HasPrefix(q, "SELECT reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to FROM deliveries") {
				queries = append(queries, q)
			}
			return true
		})
	}
	if len(queries) != 3 {
		t.Fatalf("queries: %v", queries)
	}
	return queries
}

func TestEndTurnHistoryPlans(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, q := range historyQueries(t) {
		rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+q, "session", "s", "active")
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatal(err)
			}
			plan += d + "\n"
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		index := "deliveries_open"
		if i == 2 {
			index = "deliveries_by_turn"
		}
		// The full turn index is also bounded for the updates, and SQLite can prefer it.
		if !strings.Contains(plan, index) && !(i < 2 && strings.Contains(plan, "deliveries_by_turn")) {
			t.Fatalf("%s: %s", q, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatal(plan)
		}
	}
	// Without the new index both taken-only updates must still use the open partial index.
	if _, err := s.writer.Exec("DROP INDEX deliveries_by_turn"); err != nil {
		t.Fatal(err)
	}
	for _, q := range historyQueries(t)[:2] {
		rows, err := s.writer.Query("EXPLAIN QUERY PLAN "+q, "session", "s", "active")
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var a, b, c int
			var d string
			rows.Scan(&a, &b, &c, &d)
			plan += d
		}
		rows.Close()
		if !strings.Contains(plan, "deliveries_open") {
			t.Fatal(plan)
		}
	}
}

func TestEndTurnHistoricalReadReplies(t *testing.T) {
	for _, outcome := range []contract.TurnOutcome{contract.TurnCompleted, contract.TurnFailed, contract.TurnStopped} {
		t.Run(string(outcome), func(t *testing.T) {
			channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
			s, _ := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnFeed})
			// Preserve an already-read current input and unrelated historical read input.
			if _, err := s.writer.Exec(`UPDATE deliveries SET state='read' WHERE to_id='child'; INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn) SELECT to_kind,to_id,(SELECT seq FROM events WHERE id='reply'),surface,'none','','','','',0,'read','old' FROM deliveries WHERE to_id='child' LIMIT 1`); err != nil {
				t.Fatal(err)
			}
			body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: outcome})
			if len(body.Replies) != 1 {
				t.Fatalf("lost read reply: %+v", body)
			}
			var state, turn string
			if err := s.Read().QueryRow("SELECT state,turn FROM deliveries WHERE to_id='child' AND seq=(SELECT seq FROM events WHERE id='reply')").Scan(&state, &turn); err != nil {
				t.Fatal(err)
			}
			if state != "read" || turn != "old" {
				t.Fatalf("history changed: %s %s", state, turn)
			}
			var current string
			var wake int
			if err := s.Read().QueryRow("SELECT state,wake FROM deliveries WHERE to_id='child' AND seq=(SELECT seq FROM events WHERE id='input0')").Scan(&current, &wake); err != nil {
				t.Fatal(err)
			}
			if outcome == contract.TurnFailed {
				if current != "pending" || wake != 0 {
					t.Fatalf("read input not handed back: %s %d", current, wake)
				}
			} else if current != "read" {
				t.Fatalf("input changed: %s", current)
			}
		})
	}
}

func TestDeliveriesByTurnUpgrade(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.Exec("DROP INDEX deliveries_by_turn"); err != nil {
		t.Fatal(err)
	}
	// Use the store's actual metadata key, not a hard-coded assumption.
	if _, err = s.writer.Exec("ALTER TABLE workflow_runs DROP COLUMN released_steps; ALTER TABLE sessions DROP COLUMN advisor; ALTER TABLE workflow_runs DROP COLUMN paused_steps; ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='107' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	if err = s.Read().QueryRow("SELECT name FROM sqlite_master WHERE name='deliveries_by_turn'").Scan(&name); err != nil {
		t.Fatal(err)
	}
}

// Native modernc SQLite, actual schema, isolated in-memory query costs. No FK parent fixture.
func BenchmarkEndTurnHistory(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("rows=%d/index=%t", n, indexed), func(b *testing.B) {
				db, err := sql.Open("sqlite", ":memory:")
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				db.SetMaxOpenConns(1)
				schema, err := os.ReadFile("schema.sql")
				if err != nil {
					b.Fatal(err)
				}
				if _, err = db.Exec(string(schema)); err != nil {
					b.Fatal(err)
				}
				if !indexed {
					if _, err = db.Exec("DROP INDEX deliveries_by_turn"); err != nil {
						b.Fatal(err)
					}
				}
				start := time.Now()
				_, err = db.Exec(`WITH RECURSIVE x(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM x WHERE i<?) INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn) SELECT 'session','s',i,'session','none','','','','',0,CASE WHEN i=? THEN 'taken' ELSE 'read' END,CASE WHEN i=? THEN 'active' ELSE 'old'||i END FROM x`, n+1, n+1, n+1)
				if err != nil {
					b.Fatal(err)
				}
				insert := time.Since(start)
				var pages, size int
				db.QueryRow("PRAGMA page_count").Scan(&pages)
				db.QueryRow("PRAGMA page_size").Scan(&size)
				b.Logf("insert=%s allocated_bytes=%d", insert, pages*size)
				for i, q := range historyQueries(b) {
					if !indexed {
						q = strings.ReplaceAll(q, " AND state IN ('pending','taken')", "")
					}
					b.Run(fmt.Sprint(i), func(b *testing.B) {
						samples := make([]int64, 0, b.N)
						for j := 0; j < b.N; j++ {
							db.Exec("SAVEPOINT sample")
							start := time.Now()
							rows, err := db.Query(q, "session", "s", "active")
							if err != nil {
								b.Fatal(err)
							}
							for rows.Next() {
							}
							if err = rows.Err(); err != nil {
								b.Fatal(err)
							}
							rows.Close()
							elapsed := time.Since(start)
							db.Exec("ROLLBACK TO sample")
							db.Exec("RELEASE sample")
							samples = append(samples, elapsed.Nanoseconds())
						}
						slices.Sort(samples)
						b.ReportMetric(float64(samples[len(samples)/2]), "median-ns")
						b.ReportMetric(float64(samples[(len(samples)-1)*95/100]), "p95-ns")
					})
				}
			})
		}
	}
}
