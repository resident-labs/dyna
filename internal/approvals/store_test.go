package approvals

import (
	"context"
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigrationDropsApprovalRules(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { database.Close(dataDir) })
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(db, "approvals.schema", 2, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE approval_rules (seq INTEGER PRIMARY KEY AUTOINCREMENT, decide TEXT NOT NULL)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='approval_rules'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("approval_rules tables = %d, %v", tables, err)
	}
}
