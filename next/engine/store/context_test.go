package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var contextQueries atomic.Int64

func init() { sql.Register("context-count", countingDriver{Driver: &sqlite.Driver{}}) }

type countingDriver struct{ *sqlite.Driver }
type countingConn struct{ driver.Conn }

func (d countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{conn}, nil
}
func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	contextQueries.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestContextQueryBudget(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(context.Background(), "r", "r", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
		INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("context-count", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.read.Close(); err != nil {
		t.Fatal(err)
	}
	s.read = db
	contextQueries.Store(0)
	if _, err = s.ContextDataForSession(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if got := contextQueries.Load(); got != 7 {
		t.Fatalf("snapshot queries = %d, want 7", got)
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT started_at FROM turns WHERE session=? ORDER BY started_at DESC LIMIT 1`, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		found = found || strings.Contains(detail, "turns_session_started_at")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("latest turn query did not use its index")
	}
}
