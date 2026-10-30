package roomsvc

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestWorkflowTypesFileMatchesGo(t *testing.T) {
	const output = "web/src/lib/api/workflow.gen.ts"
	const filename = "../../" + output
	const update = "DYNA_UPDATE_WORKFLOW_TYPES=1 go test ./internal/roomsvc/ -run WorkflowTypes"
	want, err := generateTypes(output, update)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DYNA_UPDATE_WORKFLOW_TYPES") != "" {
		if err := os.WriteFile(filename, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run %s (read error: %v)", filename, update, err)
	}
}

// A zero time.Time is present even when the run has not ended.
func TestWorkflowSummaryZeroEndTimeJSON(t *testing.T) {
	data, err := json.Marshal(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"endedAt":"0001-01-01T00:00:00Z"`)) {
		t.Fatalf("zero end time is missing: %s", data)
	}
}
