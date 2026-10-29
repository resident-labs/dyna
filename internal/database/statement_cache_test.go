package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

type connectionCapabilities interface {
	sqliteConn
	driver.ConnPrepareContext
	driver.ConnBeginTx
	driver.Pinger
	driver.SessionResetter
	driver.Validator
	driver.ExecerContext
	driver.QueryerContext
}
type countingConn struct {
	connectionCapabilities
	prepares int
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.prepares++
	return c.connectionCapabilities.PrepareContext(ctx, query)
}

type countingDriver struct {
	base driver.Driver
	conn *countingConn
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	d.conn = &countingConn{connectionCapabilities: conn.(connectionCapabilities)}
	return d.conn, nil
}

type testConnector struct{ d driver.Driver }

func (c testConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(":memory:") }
func (c testConnector) Driver() driver.Driver                        { return c.d }

func cacheTestDB(t *testing.T) (*sql.DB, *countingDriver) {
	t.Helper()
	base, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	count := &countingDriver{base: base.Driver()}
	db := sql.OpenDB(testConnector{d: &cacheDriver{driver: count}})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, count
}

func TestStatementCacheReusesQuery(t *testing.T) {
	db, count := cacheTestDB(t)
	for i := 0; i < 2; i++ {
		var value int
		if err := db.QueryRow("SELECT ?", i).Scan(&value); err != nil || value != i {
			t.Fatalf("value %d, error %v", value, err)
		}
	}
	if count.conn.prepares != 1 {
		t.Fatalf("prepares = %d, want 1", count.conn.prepares)
	}
}

func TestStatementCacheBusyRows(t *testing.T) {
	db, count := cacheTestDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT ? UNION ALL SELECT ?", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing first row")
	}
	var first int
	if err := rows.Scan(&first); err != nil {
		t.Fatal(err)
	}
	var second int
	if err := tx.QueryRowContext(ctx, "SELECT ? UNION ALL SELECT ?", 3, 4).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("first rows were reset")
	}
	var next int
	if err := rows.Scan(&next); err != nil {
		t.Fatal(err)
	}
	if first != 1 || next != 2 || second != 3 || count.conn.prepares != 2 {
		t.Fatalf("values %d %d %d, prepares %d", first, next, second, count.conn.prepares)
	}
}

func TestStatementCacheSchemaChange(t *testing.T) {
	db, _ := cacheTestDB(t)
	if _, err := db.Exec("CREATE TABLE example (a INTEGER, b INTEGER)"); err != nil {
		t.Fatal(err)
	}
	var value int
	if err := db.QueryRow("SELECT b FROM example").Scan(&value); err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE example DROP COLUMN b"); err != nil {
		t.Fatal(err)
	}
	cachedErr := db.QueryRow("SELECT b FROM example").Scan(&value)
	freshErr := db.QueryRow("SELECT b FROM example ").Scan(&value)
	if cachedErr == nil || freshErr == nil || !strings.Contains(cachedErr.Error(), "no such column: b") || cachedErr.Error() != freshErr.Error() {
		t.Fatalf("cached: %v, fresh: %v", cachedErr, freshErr)
	}
}

func TestStatementCachePragmaTracksSetting(t *testing.T) {
	db, count := cacheTestDB(t)
	var before, after int
	if err := db.QueryRow("/* setting */ pragma busy_timeout").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"PRAGMA busy_timeout = 123", "PRAGMA busy_timeout = 456", "PRAGMA busy_timeout = 123"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow("/* setting */ pragma busy_timeout").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 123 {
		t.Fatalf("busy_timeout before %d, after %d; want 123", before, after)
	}
	if count.conn.prepares != 0 {
		t.Fatalf("cached %d PRAGMA statements; want none", count.conn.prepares)
	}
}
