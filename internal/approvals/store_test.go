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
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE approval_rules (seq INTEGER PRIMARY KEY AUTOINCREMENT, decide TEXT NOT NULL); CREATE TABLE approval_requests(room TEXT NOT NULL,id TEXT NOT NULL); CREATE TABLE approval_decisions(seq INTEGER PRIMARY KEY, request TEXT, actor TEXT, action TEXT, reason TEXT, at INTEGER); INSERT INTO approval_requests VALUES('alpha','A-1'),('alpha','A-2'),('beta','A-2'); INSERT INTO approval_decisions VALUES(1,'A-1','actor','auto','',1),(2,'A-2','actor','pass','',2)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var room, action string
	if err := db.QueryRow(`SELECT room,action FROM approval_decisions WHERE seq=1`).Scan(&room, &action); err != nil || room != "alpha" || action != string(ActionAuto) {
		t.Fatalf("legacy auto = %q %q, %v", room, action, err)
	}
	if err := db.QueryRow(`SELECT room FROM approval_decisions WHERE seq=2`).Scan(&room); err != nil || room != "" {
		t.Fatalf("ambiguous room = %q, %v", room, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='approval_rules'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("approval_rules tables = %d, %v", tables, err)
	}
}
