// Package preferences keeps the preference-layer index: one row per account
// names the settings layer imported for it. The layer files themselves stay
// files, because each is an immutable blob.
package preferences

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
	_, err := open(dataDir)
	return err
}

func open(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := state.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "harness.preferences", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	})
	return db, err
}

// importIndex imports harness-preferences.json when the previous release
// wrote it. Its file holds every account, so it replaces the index.
//
// TODO: legacy/shim remove later — the previous release reads and writes
// harness-preferences.json; remove this import and the copy in Save with
// requirement 31, when no binary from before dyna.db is alive.
func importIndex(ctx context.Context, db *sql.DB, metadata string) error {
	_, err := state.For(db).ImportIfForeign(ctx, mirrorKey, metadata, func(conn *sql.Conn, raw []byte) error {
		var refs map[string]Ref
		if err := json.Unmarshal(raw, &refs); err != nil {
			return fmt.Errorf("preference index %s: %v: %w", metadata, err, state.ErrSkipForeign)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM harness_preferences"); err != nil {
			return err
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

// Save records account's layer in the order state.Mirror needs: import the
// previous release's file at metadata, write the row, then copy the whole
// index. The CLI and the engine both import preferences, so the index lock
// covers all three steps.
func Save(metadata, account string, ref Ref) error {
	ctx := context.Background() // Until callers pass one.
	unlock, err := store.LockContext(ctx, metadata+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	db, err := open(store.DataDir())
	if err != nil {
		return err
	}
	if err := importIndex(ctx, db, metadata); err != nil {
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

// For reads account's layer, importing the previous release's file at
// metadata first; ok is false when the account has none. It imports without
// the index lock. That is safe: ImportIfForeign checks the stamp again in its
// own transaction, and every writer imports first, so a reader can only
// import a file that a writer would have imported too.
func For(metadata, account string) (ref Ref, ok bool, err error) {
	db, err := open(store.DataDir())
	if err != nil {
		return ref, false, err
	}
	ctx := context.Background()
	if err := importIndex(ctx, db, metadata); err != nil {
		return ref, false, err
	}
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
