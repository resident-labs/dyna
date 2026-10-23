package projection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/workeroutput"
)

const schema = `CREATE TABLE output_projection (
 artifact_key TEXT NOT NULL,
 source_start INTEGER NOT NULL CHECK(source_start>=0),
 source_end INTEGER NOT NULL CHECK(source_end>source_start),
 digest BLOB NOT NULL,
 canonical_digest BLOB NOT NULL,
 pack TEXT NOT NULL,
 canonical_start INTEGER NOT NULL CHECK(canonical_start>=0),
 canonical_end INTEGER NOT NULL CHECK(canonical_end>canonical_start),
 PRIMARY KEY(artifact_key, source_start)
) WITHOUT ROWID;
CREATE TABLE output_projection_artifacts (
 artifact_key TEXT PRIMARY KEY,
 captured_size INTEGER NOT NULL,
 prefix_end INTEGER NOT NULL CHECK(prefix_end>=0 AND prefix_end<=captured_size),
 pack TEXT NOT NULL
) WITHOUT ROWID;`

// Migrate creates the inactive projection tables through the migration writer.
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "output_projection.schema", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
}

// Commit records mappings and the captured prefix on the caller's migration
// connection. The caller must roll back the transaction if Commit fails.
func (p *Prepared) Commit(ctx context.Context, conn *sql.Conn) error {
	var captured, prefix int64
	var pack string
	err := conn.QueryRowContext(ctx, "SELECT captured_size,prefix_end,pack FROM output_projection_artifacts WHERE artifact_key=?", p.Key).Scan(&captured, &prefix, &pack)
	if err == nil {
		if captured != p.CapturedSize || prefix != p.PrefixEnd || pack != p.Pack {
			return fmt.Errorf("output projection for %s has a different checkpoint", p.Key)
		}
		return p.sameRows(ctx, conn)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO output_projection_artifacts VALUES(?,?,?,?)", p.Key, p.CapturedSize, p.PrefixEnd, p.Pack); err != nil {
		return err
	}
	return p.EachRow(func(r Row) error {
		_, err := conn.ExecContext(ctx, "INSERT INTO output_projection VALUES(?,?,?,?,?,?,?,?)", p.Key, r.Start, r.End, r.SourceDigest[:], r.CanonicalDigest[:], p.Pack, r.CanonicalStart, r.CanonicalEnd)
		return err
	})
}

func (p *Prepared) sameRows(ctx context.Context, conn *sql.Conn) error {
	var stored, seen int64
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM output_projection WHERE artifact_key=?", p.Key).Scan(&stored); err != nil {
		return err
	}
	err := p.EachRow(func(r Row) error {
		var matches int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM output_projection WHERE artifact_key=? AND source_start=? AND source_end=?
AND digest=? AND canonical_digest=? AND pack=? AND canonical_start=? AND canonical_end=?`, p.Key, r.Start, r.End, r.SourceDigest[:], r.CanonicalDigest[:], p.Pack, r.CanonicalStart, r.CanonicalEnd).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			return fmt.Errorf("output projection for %s has different mappings", p.Key)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != stored {
		return fmt.Errorf("output projection for %s has different mappings", p.Key)
	}
	return nil
}

// Converted checks a stable artifact key before a migration prepares any data.
func Converted(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (bool, error) {
	var has bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='output_projection_artifacts')").Scan(&has); err != nil || !has {
		return false, err
	}
	err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM output_projection_artifacts WHERE artifact_key=?)", key).Scan(&has)
	return has, err
}

// Reader resolves immutable pack bytes beside the artifact's current path.
type Reader struct {
	dir  string
	rows map[int64]readRow
	pack *os.File
}

type readRow struct {
	Row
	pack string
}

// Open reads mappings without creating tables or converting historical output.
func Open(dataDir, key, path string) (*Reader, error) {
	r := &Reader{dir: filepath.Dir(path), rows: map[int64]readRow{}}
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	var has bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='output_projection')").Scan(&has); err != nil || !has {
		return r, err
	}
	rows, err := db.Query("SELECT source_start,source_end,digest,canonical_digest,pack,canonical_start,canonical_end FROM output_projection WHERE artifact_key=?", key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x readRow
		var source, canonical []byte
		if err := rows.Scan(&x.Start, &x.End, &source, &canonical, &x.pack, &x.CanonicalStart, &x.CanonicalEnd); err != nil {
			return nil, err
		}
		if len(source) != sha256.Size || len(canonical) != sha256.Size || filepath.Base(x.pack) != x.pack || x.pack == "." {
			return nil, errors.New("invalid output projection mapping")
		}
		copy(x.SourceDigest[:], source)
		copy(x.CanonicalDigest[:], canonical)
		r.rows[x.Start] = x
	}
	return r, rows.Err()
}

// Event resolves a complete source line without changing its source cursor.
// A changed source or pack is an error, never a stale converted event.
func (r *Reader) Event(start, end int64, raw []byte) (e workeroutput.Event, ok bool, err error) {
	x, found := r.rows[start]
	if !found {
		return e, false, nil
	}
	if x.End != end || sha256.Sum256(raw) != x.SourceDigest {
		return e, false, fmt.Errorf("converted output line at offset %d changed", start)
	}
	if r.pack == nil || filepath.Base(r.pack.Name()) != x.pack {
		if r.pack != nil {
			r.pack.Close()
			r.pack = nil
		}
		r.pack, err = os.Open(filepath.Join(r.dir, x.pack))
		if err != nil {
			return e, false, err
		}
	}
	info, err := r.pack.Stat()
	if err != nil {
		return e, false, err
	}
	if x.CanonicalStart < 0 || x.CanonicalEnd <= x.CanonicalStart || x.CanonicalEnd > info.Size() {
		return e, false, errors.New("invalid canonical output range")
	}
	b := make([]byte, x.CanonicalEnd-x.CanonicalStart)
	if _, err := r.pack.ReadAt(b, x.CanonicalStart); err != nil {
		return e, false, err
	}
	if sha256.Sum256(b) != x.CanonicalDigest {
		return e, false, errors.New("canonical output digest changed")
	}
	if err := json.Unmarshal(bytes.TrimSuffix(b, []byte("\n")), &e); err != nil {
		return e, false, err
	}
	return e, true, nil
}

func (r *Reader) Close() error {
	if r.pack == nil {
		return nil
	}
	return r.pack.Close()
}
