package database

import (
	"container/list"
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"sync"

	"modernc.org/sqlite"
)

const statementCacheLimit = 128

func init() {
	db, err := sql.Open("sqlite", "")
	if err != nil {
		panic(err)
	}
	sql.Register("dyna-sqlite", &cacheDriver{driver: db.Driver()})
	_ = db.Close()
}

type cacheDriver struct{ driver driver.Driver }

func (d *cacheDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	return &cacheConn{sqliteConn: conn.(sqliteConn), entries: make(map[string]*list.Element)}, nil
}

type cacheEntry struct {
	query string
	stmt  driver.Stmt
	busy  bool
}

type sqliteConn interface {
	driver.Conn
	driver.Execer
	driver.Queryer
	sqlite.FileControl
	sqlite.DBStatus
	sqlite.HookRegisterer
	ColumnInfo(string) ([]sqlite.ColumnInfo, error)
	Serialize() ([]byte, error)
	Deserialize([]byte) error
	NewBackup(string) (*sqlite.Backup, error)
	NewRestore(string) (*sqlite.Backup, error)
	IsReadOnly(string) (bool, error)
}

type cacheConn struct {
	sqliteConn
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
}

func (c *cacheConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.sqliteConn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}
func (c *cacheConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.sqliteConn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *cacheConn) Ping(ctx context.Context) error { return c.sqliteConn.(driver.Pinger).Ping(ctx) }
func (c *cacheConn) ResetSession(ctx context.Context) error {
	return c.sqliteConn.(driver.SessionResetter).ResetSession(ctx)
}
func (c *cacheConn) IsValid() bool { return c.sqliteConn.(driver.Validator).IsValid() }

func (c *cacheConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.lru.Front(); e != nil; e = e.Next() {
		_ = e.Value.(*cacheEntry).stmt.Close()
	}
	c.entries = nil
	c.lru.Init()
	return c.sqliteConn.Close()
}

// acquire keeps a statement reserved until execution or rows closure.
func (c *cacheConn) acquire(ctx context.Context, query string) (*cacheEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[query]; e != nil {
		entry := e.Value.(*cacheEntry)
		if !entry.busy {
			entry.busy = true
			c.lru.MoveToFront(e)
			return entry, true, nil
		}
	}
	stmt, err := c.sqliteConn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	entry := &cacheEntry{query: query, stmt: stmt, busy: true}
	if c.entries[query] != nil {
		return entry, false, nil
	}
	if c.lru.Len() == statementCacheLimit {
		e := c.lru.Back()
		for e != nil && e.Value.(*cacheEntry).busy {
			e = e.Prev()
		}
		if e == nil {
			return entry, false, nil
		}
		old := e.Value.(*cacheEntry)
		delete(c.entries, old.query)
		c.lru.Remove(e)
		_ = old.stmt.Close()
	}
	c.entries[query] = c.lru.PushFront(entry)
	return entry, true, nil
}

func (c *cacheConn) release(entry *cacheEntry, cached, failed bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !cached {
		return entry.stmt.Close()
	}
	if failed {
		e := c.entries[entry.query]
		delete(c.entries, entry.query)
		c.lru.Remove(e)
		return entry.stmt.Close()
	}
	entry.busy = false
	return nil
}

// Scripts and PRAGMAs may do work at prepare time and must not be reused.
// Match conservatively, including PRAGMA in comments or quoted text.
func bypassStatementCache(query string) bool {
	return strings.Contains(query, ";") || strings.Contains(strings.ToUpper(query), "PRAGMA")
}

func (c *cacheConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if bypassStatementCache(query) {
		return c.sqliteConn.(driver.ExecerContext).ExecContext(ctx, query, args)
	}
	entry, cached, err := c.acquire(ctx, query)
	if err != nil {
		return nil, err
	}
	result, err := entry.stmt.(driver.StmtExecContext).ExecContext(ctx, args)
	closeErr := c.release(entry, cached, err != nil)
	if err == nil {
		err = closeErr
	}
	return result, err
}

func (c *cacheConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if bypassStatementCache(query) {
		return c.sqliteConn.(driver.QueryerContext).QueryContext(ctx, query, args)
	}
	entry, cached, err := c.acquire(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := entry.stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
	if err != nil {
		_ = c.release(entry, cached, true)
		return nil, err
	}
	return &cachedRows{Rows: rows, conn: c, entry: entry, cached: cached}, nil
}

type cachedRows struct {
	driver.Rows
	conn   *cacheConn
	entry  *cacheEntry
	cached bool
}

func (r *cachedRows) Close() error {
	err := r.Rows.Close()
	closeErr := r.conn.release(r.entry, r.cached, err != nil)
	if err != nil {
		return err
	}
	return closeErr
}
func (r *cachedRows) ColumnTypeDatabaseTypeName(i int) string {
	return r.Rows.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(i)
}
func (r *cachedRows) ColumnTypeLength(i int) (int64, bool) {
	return r.Rows.(driver.RowsColumnTypeLength).ColumnTypeLength(i)
}
func (r *cachedRows) ColumnTypeNullable(i int) (bool, bool) {
	return r.Rows.(driver.RowsColumnTypeNullable).ColumnTypeNullable(i)
}
func (r *cachedRows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	return r.Rows.(driver.RowsColumnTypePrecisionScale).ColumnTypePrecisionScale(i)
}
func (r *cachedRows) ColumnTypeScanType(i int) reflect.Type {
	return r.Rows.(driver.RowsColumnTypeScanType).ColumnTypeScanType(i)
}
