package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestCounterCommitsAndRollsBackWithWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(dir)
	if err := Immediate(ctx, db, func(conn *sql.Conn) error {
		if err := CreateRevisions(ctx, conn); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "CREATE TABLE entries(n INTEGER)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c := NewCounter("test")
	stop := errors.New("rollback")
	write := func(fail bool) error {
		return Immediate(ctx, db, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, "INSERT INTO entries VALUES(1)"); err != nil {
				return err
			}
			if err := c.Bump(ctx, conn); err != nil {
				return err
			}
			if fail {
				return stop
			}
			return nil
		})
	}
	if err := write(false); err != nil {
		t.Fatal(err)
	}
	if err := write(true); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	n, err := c.Read(ctx, db)
	if err != nil || n != 1 {
		t.Fatalf("counter=%d err=%v", n, err)
	}
	var rows int
	if err := db.QueryRow("SELECT count(*) FROM entries").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}
