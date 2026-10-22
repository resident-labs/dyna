// Package preferences keeps the preference-layer index: one row per account
// names the settings layer imported for it. The layer files themselves stay
// files, because each is an immutable blob.
package preferences

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// The index lives in dyna.db (rule r10).
const schema = `CREATE TABLE harness_preferences (
 account TEXT PRIMARY KEY,
 ref BLOB NOT NULL CHECK(json_valid(ref))
);`

// mirrorKey names harness-preferences.json in state_mirrors.
const mirrorKey = "harness/preferences"

// Ref names one account's imported settings layer.
type Ref struct {
	Settings   string `json:"settings"`
	SourceHash string `json:"sourceHash"`
	Harness    string `json:"harness"`
}

// Migrate creates the preference index table. Only the engine runs it, at
// start.
func Migrate(dataDir string) error {
	return MigrateFrom(dataDir, filepath.Join(store.ConfigDir(), "harness-preferences.json"))
}

// MigrateFrom imports the index beside a custom catalog before its version advances.
func MigrateFrom(dataDir, metadata string) error {
	_, err := open(dataDir, metadata)
	return err
}

func open(dataDir, metadata string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "harness.preferences", 2, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, schema); err != nil {
				return err
			}
		}
		return importIndex(ctx, conn, metadata)
	})
	return db, err
}

// TODO: legacy/shim remove later — import the retained whole index only at migration.
func importIndex(ctx context.Context, conn *sql.Conn, metadata string) error {
	var canonical bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM harness_preferences) OR EXISTS(SELECT 1 FROM state_mirrors WHERE key = ? AND (version != '' OR COALESCE(pending, '') != ''))`, mirrorKey).Scan(&canonical); err != nil {
		return err
	}
	// A missing account in a canonical index is a deletion, not a gap to fill.
	if canonical {
		return nil
	}
	_, err := state.ImportMigration(ctx, conn, mirrorKey, metadata, func(conn *sql.Conn, raw []byte) error {
		var refs map[string]Ref
		if json.Unmarshal(raw, &refs) != nil {
			return nil
		}
		for account, ref := range refs {
			if err := saveRow(ctx, conn, account, ref); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func saveRow(ctx context.Context, conn *sql.Conn, account string, ref Ref) error {
	raw, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO harness_preferences(account, ref) VALUES (?, ?)
ON CONFLICT(account) DO UPDATE SET ref = excluded.ref`, account, raw)
	return err
}

// Save records account's layer and copies the whole index for the previous release.
func Save(metadata, account string, ref Ref) error {
	ctx := context.Background() // Until callers pass one.
	unlock, err := store.LockContext(ctx, metadata+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	db, err := open(store.DataDir(), metadata)
	if err != nil {
		return err
	}
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return saveRow(ctx, conn, account, ref)
	}); err != nil {
		return err
	}
	// TODO: legacy/shim remove later — harness-preferences.json is the
	// previous release's copy of the whole index; remove with requirement 31.
	return state.For(db).Mirror(ctx, mirrorKey, metadata, func(out io.Writer) error {
		refs, err := readIndex(ctx, db)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(refs, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(append(b, '\n'))
		return err
	})
}

// readIndex reads every account's layer.
func readIndex(ctx context.Context, db *sql.DB) (map[string]Ref, error) {
	refs := map[string]Ref{}
	err := state.For(db).Read(ctx, func(q state.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT account, ref FROM harness_preferences")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var account string
			var raw []byte
			if err := rows.Scan(&account, &raw); err != nil {
				return err
			}
			var ref Ref
			if err := json.Unmarshal(raw, &ref); err != nil {
				return err
			}
			refs[account] = ref
		}
		return rows.Err()
	})
	return refs, err
}

// For reads account's layer; ok is false when the account has none.
func For(metadata, account string) (ref Ref, ok bool, err error) {
	db, err := open(store.DataDir(), metadata)
	if err != nil {
		return ref, false, err
	}
	ctx := context.Background()
	err = state.For(db).Read(ctx, func(q state.Reader) error {
		var raw []byte
		err := q.QueryRowContext(ctx, "SELECT ref FROM harness_preferences WHERE account = ?", account).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = true
		return json.Unmarshal(raw, &ref)
	})
	return ref, ok, err
}

// Forget deletes account's row inside the caller's write, the account
// store's removal. The copy keeps naming the account until the next Save;
// the previous release cannot use a removed account, so that is harmless.
func Forget(ctx context.Context, conn *sql.Conn, account string) error {
	ok, err := database.HasTable(ctx, conn, "harness_preferences")
	if err != nil || !ok {
		return err
	}
	_, err = conn.ExecContext(ctx, "DELETE FROM harness_preferences WHERE account = ?", account)
	return err
}
