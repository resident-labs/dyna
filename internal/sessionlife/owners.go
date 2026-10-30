package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"sync"

	"dyna-agent/internal/database"
)

// The ownership projection: worktree ownership inference needs each record's
// dir, service, room, name and closed state, but not the rest of the record.
// Reading those as typed columns, written with the meta blob in the same
// statement, replaces the legacy read that JSON-decoded every meta row.
// meta_digest detects a meta blob that changed after its columns were
// written, so damaged or externally edited records still fail closed.

// ownerDigestTable is the Castagnoli CRC of a stored meta blob. Integrity,
// not secrecy, is the goal; the checksum runs at line rate on this path.
var ownerDigestTable = crc32.MakeTable(crc32.Castagnoli)

// ownerDigest is the stored digest of raw, the exact meta bytes written with
// the ownership columns.
func ownerDigest(raw []byte) string {
	return strconv.FormatUint(uint64(crc32.Checksum(raw, ownerDigestTable)), 16)
}

// OwnerRow is one session's typed ownership evidence.
type OwnerRow struct {
	ID, Room, Name, Dir string
	Service, Closed     bool
}

// ownerCaches keeps only verified projections, not metadata blobs. The database
// handle separates stores and the delete counter prevents revision reuse.
var ownerCaches sync.Map // map[*sql.DB]*ownerCache

func init() { database.OnClose(func(db *sql.DB) { ownerCaches.Delete(db) }) }

type cachedOwner struct {
	revision int64
	owner    OwnerRow
}

type ownerCache struct {
	mu      sync.Mutex
	deletes string
	rows    map[string]cachedOwner
}

// Owners visits every session's verified ownership row in ID order. A changed
// row is verified before it enters the cache; any failure keeps trees safe.
// A raw edit of meta or digest that keeps the revision is outside the contract.
func Owners(sessions string, visit func(OwnerRow) error) error {
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return err
	}
	value, _ := ownerCaches.LoadOrStore(db, &ownerCache{})
	cache := value.(*ownerCache)
	cache.mu.Lock()
	rows, err := cache.read(db)
	cache.mu.Unlock()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

func (c *ownerCache) read(db *sql.DB) ([]OwnerRow, error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var deletes string
	if err := tx.QueryRow("SELECT COALESCE((SELECT value FROM meta WHERE key = ?), '0')", DeletesKey).Scan(&deletes); err != nil {
		return nil, err
	}
	if c.deletes != deletes {
		c.rows = nil
	}
	var rows []OwnerRow
	next := make(map[string]cachedOwner)
	if len(c.rows) == 0 {
		res, err := tx.Query("SELECT id, revision, meta_id, room, name, closed, dir, service, meta, meta_digest FROM session_records ORDER BY id")
		if err != nil {
			return nil, err
		}
		for res.Next() {
			var id, metaID, digest string
			var revision int64
			var row OwnerRow
			var closed int
			var meta sql.RawBytes
			if err := res.Scan(&id, &revision, &metaID, &row.Room, &row.Name, &closed, &row.Dir, &row.Service, &meta, &digest); err != nil {
				res.Close()
				return nil, err
			}
			row.ID, row.Closed = id, closed != 0
			if err := verifyOwner(id, metaID, digest, meta); err != nil {
				res.Close()
				return nil, err
			}
			next[id] = cachedOwner{revision: revision, owner: row}
			rows = append(rows, row)
		}
		if err := errors.Join(res.Err(), res.Close()); err != nil {
			return nil, err
		}
	} else {
		res, err := tx.Query("SELECT id, revision FROM session_records ORDER BY id")
		if err != nil {
			return nil, err
		}
		type version struct {
			id       string
			revision int64
			length   int
		}
		var versions []version
		for res.Next() {
			var v version
			if err := res.Scan(&v.id, &v.revision); err != nil {
				res.Close()
				return nil, err
			}
			versions = append(versions, v)
		}
		if err := errors.Join(res.Err(), res.Close()); err != nil {
			return nil, err
		}
		for _, v := range versions {
			r, ok := c.rows[v.id]
			if !ok || r.revision != v.revision {
				var metaID, digest string
				var meta []byte
				var closed int
				r = cachedOwner{revision: v.revision}
				r.owner.ID = v.id
				err := tx.QueryRow("SELECT meta_id, room, name, closed, dir, service, meta, meta_digest FROM session_records WHERE id = ?", v.id).Scan(&metaID, &r.owner.Room, &r.owner.Name, &closed, &r.owner.Dir, &r.owner.Service, &meta, &digest)
				if err != nil {
					return nil, err
				}
				r.owner.Closed = closed != 0
				if err := verifyOwner(v.id, metaID, digest, meta); err != nil {
					return nil, err
				}
			}
			next[v.id] = r
			rows = append(rows, r.owner)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.rows, c.deletes = next, deletes
	return rows, nil
}

func verifyOwner(id, metaID, digest string, meta []byte) error {
	if metaID != "" && metaID != id {
		return fmt.Errorf("%w: session %s record names id %s", ErrRecord, id, metaID)
	}
	if digest == "" || digest != ownerDigest(meta) {
		return fmt.Errorf("%w: session %s meta does not match its stored columns", ErrRecord, id)
	}
	return nil
}

// backfillOwnerColumns adds the ownership columns to an existing store, at
// schema version 4, and imports each row's values from its meta blob once.
// A row whose meta is damaged keeps an empty digest and stays fail-closed
// until the record is rewritten or removed; a row whose meta names another
// ID keeps that ID, which Owners rejects.
func backfillOwnerColumns(ctx context.Context, conn *sql.Conn) error {
	for _, column := range []struct{ name, kind string }{
		{"dir", "TEXT NOT NULL DEFAULT ''"},
		{"service", "INTEGER NOT NULL DEFAULT 0"},
		{"meta_id", "TEXT NOT NULL DEFAULT ''"},
		{"meta_digest", "TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE session_records ADD COLUMN "+column.name+" "+column.kind); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT id FROM session_records ORDER BY id")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		var meta string
		if err := conn.QueryRowContext(ctx, "SELECT meta FROM session_records WHERE id = ?", id).Scan(&meta); err != nil {
			return err
		}
		raw := []byte(meta)
		var c recordColumns
		var digest string
		if isObject(raw) && len(raw) <= recordLimit && json.Unmarshal(raw, &c) == nil {
			digest = ownerDigest(raw)
		}
		service := 0
		if c.Service {
			service = 1
		}
		if _, err := conn.ExecContext(ctx, "UPDATE session_records SET dir = ?, service = ?, meta_id = ?, meta_digest = ? WHERE id = ?", c.Dir, service, c.ID, digest, id); err != nil {
			return err
		}
	}
	return nil
}
