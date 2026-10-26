package runstore

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// A store from before agents were sessions holds the steering table at its
// last version; opening the runs store drops it.
func TestOpeningRunsDropsTheSteeringTable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dataDir := store.DataDir()
	t.Cleanup(func() { database.Close(dataDir) })
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(db, "runs.steering", 2, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(t.Context(), "CREATE TABLE run_agent_steering (run TEXT, agent INTEGER)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRuns(dataDir); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='run_agent_steering'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("steering table count = %d, %v; want it dropped", n, err)
	}
}
