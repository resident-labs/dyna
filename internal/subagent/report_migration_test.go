package subagent

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestPendingReportIndexKeepsEarlierRunnerTurnSchema(t *testing.T) {
	data := storetest.DataDir(t)
	store := DefaultStore()
	if _, err := store.PendingReports(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(data); err != nil {
			t.Error(err)
		}
	})
	// Reopen before asking as an earlier runner, so a process-local migration
	// cache cannot hide an incompatible version in the actual database.
	if err := database.Migrate(db, "subagent.turns", 2, func(*sql.Conn, int) error {
		t.Error("the earlier runner unexpectedly needed a turn migration")
		return nil
	}); err != nil {
		t.Fatalf("earlier runner cannot read turns after report-index migration: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'session_turns_pending_reports'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending report index count = %d, %v; want one", count, err)
	}
}
