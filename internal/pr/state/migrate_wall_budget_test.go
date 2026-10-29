package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWallTimeVerdictMigrationPreservesStepFailures(t *testing.T) {
	root := newRoot(t)
	body := `{"version":1,"records":{"wall":{"key":"wall","head":"h","ci":"failed","ciReason":"over CI time budget (3m)","ciBudgetHead":"h","ciBudgetRuns":{"1":"over CI time budget (3m)"},"ciBudgetInputs":{"1":"cached"},"ciBudgetNotes":{"1":"old note"}},"step":{"key":"step","head":"h","ci":"failed","ciReason":"over CI time budget (3m)","ciBudgetHead":"h","ciBudgetRuns":{"2":"over CI time budget (3m)"},"ciBudgetSteps":{"2":true}}}}`
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		st, err := Read(root)
		if err != nil {
			t.Fatal(err)
		}
		wall, step := st.Records["wall"], st.Records["step"]
		if wall.CI != "pending" || wall.CIReason != "" || len(wall.CIBudgetRuns) != 0 || len(wall.CIBudgetInputs) != 0 || len(wall.ReviewHistory) != 1 {
			t.Fatalf("wall evidence not retired: %+v", wall)
		}
		var archive struct {
			Head  string
			Notes map[string]string
		}
		if err := json.Unmarshal(wall.ReviewHistory[0]["ciBudget"], &archive); err != nil || archive.Head != "h" || archive.Notes["1"] != "old note" {
			t.Fatalf("archive: %+v, %v", archive, err)
		}
		if step.CI != "failed" || !step.CIBudgetSteps["2"] || step.CIBudgetRuns["2"] == "" || len(step.ReviewHistory) != 0 {
			t.Fatalf("step evidence changed: %+v", step)
		}
	}
}
