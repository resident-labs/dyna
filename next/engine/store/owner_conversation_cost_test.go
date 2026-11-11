package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

const oldOwnerQuery = `SELECT d.reply_home FROM deliveries d JOIN turns t ON t.id=d.turn WHERE d.to_kind=? AND d.to_id=? AND t.session=? AND t.ended_at IS NULL AND d.reply_kind=? AND d.reply_target_kind=? ORDER BY d.seq DESC LIMIT 1`

var ownerArgs = []any{"session", "s", "s", "final", "owner"}

func ownerFixture(tb testing.TB, n int) *Store {
	tb.Helper()
	s, err := Open(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	// Isolated query-cost fixture: identities are intentionally incomplete.
	_, err = s.writer.Exec(`PRAGMA foreign_keys=OFF`)
	if err != nil {
		tb.Fatal(err)
	}
	tx, err := s.writer.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < n; i++ {
		_, err = tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,'s',0,1,'','',0,0,0,0)`, fmt.Sprint(i))
		if err != nil {
			tb.Fatal(err)
		}
		_, err = tx.Exec(`INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn) VALUES('session','s',?,'dm','final','owner','','dm:s','',1,'done',?)`, i+1, fmt.Sprint(i))
		if err != nil {
			tb.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('open','s',0,'','',0,0,0,0)`); err != nil {
		tb.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	return s
}

func TestOwnerConversationMixedEquivalence(t *testing.T) {
	s := ownerFixture(t, 80)
	for i := 0; i < 80; i++ {
		// Multiple open turns, other sessions and recipients, non-owner replies,
		// null turns and malformed homes; compare actual decoder and fallback too.
		_, err := s.writer.Exec(`UPDATE turns SET ended_at=CASE WHEN CAST(id AS INTEGER)%3=0 THEN NULL ELSE 1 END,session=CASE WHEN CAST(id AS INTEGER)%7=0 THEN 'other' ELSE 's' END;
 UPDATE deliveries SET reply_home=CASE seq%4 WHEN 0 THEN 'broken' WHEN 1 THEN 'feed' WHEN 2 THEN 'dm:s' ELSE 'dm:other' END,reply_kind=CASE WHEN seq%5=0 THEN 'none' ELSE 'final' END,reply_target_kind=CASE WHEN seq%6=0 THEN 'session' ELSE 'owner' END,to_id=CASE WHEN seq%9=0 THEN 'other' ELSE 's' END,turn=CASE WHEN seq%11=0 THEN NULL ELSE turn END`)
		if err != nil {
			t.Fatal(err)
		}
		var before, after sql.NullString
		e1 := s.Read().QueryRow(oldOwnerQuery, ownerArgs...).Scan(&before)
		e2 := s.Read().QueryRow(ownerConversationQuery, ownerArgs...).Scan(&after)
		if e1 != e2 || before != after {
			t.Fatalf("iteration %d: %v %v / %v %v", i, before, e1, after, e2)
		}
		home, err := s.OwnerConversation(context.Background(), "s")
		if err != nil {
			t.Fatal(err)
		}
		want := "feed"
		if after.String == "dm:s" {
			want = "dm"
		}
		if string(home.Kind) != want {
			t.Fatalf("home %v for %v", home, after)
		}
		if _, err = s.writer.Exec(`DELETE FROM deliveries WHERE seq=?`, 80-i); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnerConversationPlan(t *testing.T) {
	s := ownerFixture(t, 10)
	// CROSS JOIN keeps the open-turn-first order explicit even without statistics.
	rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+ownerConversationQuery, ownerArgs...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var a, b, c int
		var d string
		if err = rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(d + "\n")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log(plan.String())
	for _, index := range []string{"turns_session_ended", "deliveries_recipient_turn"} {
		if !strings.Contains(plan.String(), index) {
			t.Fatalf("missing %s: %s", index, plan.String())
		}
	}
}

func BenchmarkOwnerConversationCost(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := ownerFixture(b, n)
			for _, q := range []struct{ name, sql string }{{"before", strings.Replace(oldOwnerQuery, "deliveries d JOIN", "deliveries d INDEXED BY sqlite_autoindex_deliveries_1 JOIN", 1)}, {"after", ownerConversationQuery}} {
				b.Run(q.name, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						var home string
						err := s.Read().QueryRow(q.sql, ownerArgs...).Scan(&home)
						if err != sql.ErrNoRows {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func BenchmarkTranscriptMapCost(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := ownerFixture(b, n)
			b.Run("before", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := s.TranscriptDeliveries(context.Background(), "s"); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("after-unused", func(b *testing.B) {
				for i := 0; i < b.N; i++ { /* No map query on input-only paths. */
				}
			})
		})
	}
}

func TestOwnerConversationIndexMigration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.Exec(`DROP INDEX deliveries_recipient_turn; UPDATE meta SET value='107' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = compareSchema(s.writer); err != nil {
		t.Fatal(err)
	}
}
