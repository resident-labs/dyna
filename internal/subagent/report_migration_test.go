package subagent

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestPendingReportIndexKeepsPromptFormatBoundary(t *testing.T) {
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
	// Reopen so a process-local cache cannot hide the actual format boundary.
	if err := database.Migrate(db, "subagent.turns", promptStoreFormat, func(*sql.Conn, int) error {
		t.Error("the current reader unexpectedly needed a turn migration")
		return nil
	}); err != nil {
		t.Fatalf("current reader cannot read turns after report-index migration: %v", err)
	}
	if err := database.Migrate(db, "subagent.turns", 2, func(*sql.Conn, int) error {
		t.Error("the earlier runner unexpectedly changed the turn schema")
		return nil
	}); err == nil {
		t.Fatal("earlier runner accepted prompt references it cannot resolve")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'session_turns_pending_reports'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending report index count = %d, %v; want one", count, err)
	}
}
