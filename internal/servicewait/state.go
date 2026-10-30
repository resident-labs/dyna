// Package servicewait records independent service availability transitions.
package servicewait

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"

	"dyna-agent/internal/database"
)

// Migrate installs the service availability table at engine startup.
func Migrate(dir string) error {
	db, err := database.Open(dir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "servicewait.schema", 1, func(c *sql.Conn, _ int) error {
		_, err := c.ExecContext(context.Background(), `CREATE TABLE service_wait (scope TEXT PRIMARY KEY, state TEXT NOT NULL, token TEXT NOT NULL)`)
		return err
	})
}

// Observe returns a stable token for the current state. An empty state records
// recovery. A later recurrence receives a new token, even after a restart.
func Observe(dir, scope, state string) (string, error) {
	if err := Migrate(dir); err != nil {
		return "", err
	}
	db, err := database.Open(dir)
	if err != nil {
		return "", err
	}
	var b [16]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", err
	}
	var token string
	err = db.QueryRow(`INSERT INTO service_wait(scope,state,token) VALUES(?,?,?)
 ON CONFLICT(scope) DO UPDATE SET token=CASE WHEN state != excluded.state THEN excluded.token ELSE token END, state=excluded.state RETURNING token`, scope, state, hex.EncodeToString(b[:])).Scan(&token)
	return token, err
}
