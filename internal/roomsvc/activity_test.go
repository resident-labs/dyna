package roomsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/sessionlife/recordtest"
)

func activityRecord(t *testing.T, root, id string, meta map[string]any, turns []map[string]any) {
	t.Helper()
	meta["room"] = "test-room"
	dir := filepath.Join(root, id, "turns")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, root, id, meta)
	for i, v := range turns {
		v["n"] = i + 1
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, fmtTurn(i+1)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func fmtTurn(n int) string { return fmt.Sprintf("%d.json", n) }

func TestUsageRollupRejectsInvalidPeriod(t *testing.T) {
	e := newEnv(t)
	for _, period := range [][2]string{{"yesterday", "2026-10-17T10:00:00Z"}, {"2026-10-17T10:00:00Z", "tomorrow"}, {"2026-10-17T10:00:00Z", "2026-10-16T10:00:00Z"}, {"", ""}} {
		if _, err := e.svc.UsageRollup("", period[0], period[1]); err == nil {
			t.Fatalf("invalid period accepted: %v", period)
		}
	}
}
