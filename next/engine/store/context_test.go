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
	if got := contextQueries.Load(); got != 9 {
		t.Fatalf("snapshot queries = %d, want 9", got)
	}
}

func TestContextChildrenOrderIgnoresActivity(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(context.Background(), "r", "r", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('root','r','root','p','open','',0);
		INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('a','r','a','p','open','',1,'root'),('b','r','b','p','open','',2,'root');
		INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('ba','a','','agent','ready','','',0,0),('bb','b','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		children, err := s.ContextChildren(context.Background(), "root")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range children {
			got = append(got, c.Name)
		}
		return strings.Join(got, ",")
	}
	if got := names(); got != "a,b" {
		t.Fatalf("children = %s, want a,b", got)
	}
	// The later child's turn starts first, then the earlier child's: neither swaps the rows.
	for i, session := range []string{"b", "a"} {
		if _, err = s.writer.Exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,?,?,'','m',0,0,0,0)`, "t"+session, session, 10+i); err != nil {
			t.Fatal(err)
		}
		if got := names(); got != "a,b" {
			t.Fatalf("children after a turn of %s = %s, want a,b", session, got)
		}
	}
}
