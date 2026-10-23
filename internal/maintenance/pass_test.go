package maintenance_test

import (
	"context"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/store/storetest"
)

// Pass is what the built-in hook runs, so its pass must reach the report
// table that `dyna maintenance status` reads.
func TestPassRecordsThePassItRan(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	result, err := maintenance.Pass(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := maintenance.RecentReports(dataDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Summary != result.Summary() {
		t.Fatalf("reports = %#v, pass summary = %q", reports, result.Summary())
	}
}
