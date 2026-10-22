package subagent

import (
	"context"
	"database/sql"

	"dyna-agent/internal/database"
)

// Prompt storage is additive until the format 3 cutover. Existing turns
// continue to store inline text.
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
