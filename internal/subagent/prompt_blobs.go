package subagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
)

// Turn prompts share blobs by hash. Historical inline fields remain readable
// until the startup conversion registers their references.
const promptBlobSchema = `CREATE TABLE prompt_blobs (
 hash TEXT PRIMARY KEY,
 text TEXT NOT NULL
);
CREATE TABLE prompt_refs (
 session TEXT NOT NULL,
 n INTEGER NOT NULL,
 hash TEXT NOT NULL,
 PRIMARY KEY(session, n, hash)
);
CREATE INDEX prompt_refs_hash ON prompt_refs(hash);`

func migratePromptBlobs(db *sql.DB) error {
	return database.Migrate(db, "subagent.prompt_blobs", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), promptBlobSchema)
		return err
	})
}

// storeTurnPrompts replaces a turn's references and returns encoded prompt
// fields. The caller must write the returned row in the same transaction.
// Input contains full text, including text that happens to look like a hash.
func storeTurnPrompts(ctx context.Context, conn *sql.Conn, id string, n int, raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("turn %s/%d is not a JSON object", id, n)
	}
	rows, err := conn.QueryContext(ctx, "SELECT hash FROM prompt_refs WHERE session=? AND n=?", id, n)
	if err != nil {
		return nil, err
	}
	var old []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return nil, err
		}
		old = append(old, hash)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM prompt_refs WHERE session=? AND n=?", id, n); err != nil {
		return nil, err
	}
	for _, name := range []string{"prompt", "heldPrompt"} {
		value, ok := fields[name]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, fmt.Errorf("turn %s/%d %s: %w", id, n, name, err)
		}
		if text == "" {
			continue
		}
		hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO prompt_blobs(hash,text) VALUES (?,?)", hash, text); err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO prompt_refs(session,n,hash) VALUES (?,?,?)", id, n, hash); err != nil {
			return nil, err
		}
		fields[name], _ = json.Marshal(hash)
	}
	for _, hash := range old {
		if _, err := conn.ExecContext(ctx, "DELETE FROM prompt_blobs WHERE hash=? AND NOT EXISTS (SELECT 1 FROM prompt_refs WHERE hash=?)", hash, hash); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

// resolveRowPrompts reads through the snapshot that supplied the turn row.
// Only a reference owned by this turn can turn a field into a blob lookup.
func resolveRowPrompts(ctx context.Context, r state.Reader, id string, n int, raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("turn %s/%d is not a JSON object", id, n)
	}
	changed := false
	for _, name := range []string{"prompt", "heldPrompt"} {
		var hash string
		if json.Unmarshal(fields[name], &hash) != nil || hash == "" {
			continue
		}
		var text sql.NullString
		err := r.QueryRowContext(ctx, `SELECT b.text FROM prompt_refs r
LEFT JOIN prompt_blobs b ON b.hash=r.hash
WHERE r.session=? AND r.n=? AND r.hash=?`, id, n, hash).Scan(&text)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !text.Valid {
			return nil, fmt.Errorf("turn %s/%d prompt blob %s is missing", id, n, hash)
		}
		fields[name], _ = json.Marshal(text.String)
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(fields)
}
