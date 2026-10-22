package runstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogEventProvenanceJSONCompatibility(t *testing.T) {
	var legacy Event
	if err := json.Unmarshal([]byte(`{"t":"log","ts":1,"msg":"old line"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.T != "log" || legacy.Msg != "old line" || legacy.Provenance != "" {
		t.Fatalf("legacy event = %#v", legacy)
	}
	if legacy.IsWorkflowLog() {
		t.Fatal("legacy log without provenance must not be treated as workflow-authored")
	}

	var extra Event
	if err := json.Unmarshal([]byte(`{"t":"log","msg":"hello","provenance":"workflow","unknown":true}`), &extra); err != nil {
		t.Fatal(err)
	}
	if extra.Msg != "hello" || extra.Provenance != LogProvenanceWorkflow || !extra.IsWorkflowLog() {
		t.Fatalf("workflow event = %#v", extra)
	}

	encoded, err := json.Marshal(Event{T: "log", Msg: "hello", Provenance: LogProvenanceWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"t":"log"`) || !strings.Contains(string(encoded), `"provenance":"workflow"`) {
		t.Fatalf("workflow marshal = %s", encoded)
	}

	encoded, err = json.Marshal(Event{T: "log", Msg: "diag", Provenance: LogProvenanceSystem})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"provenance":"system"`) {
		t.Fatalf("system marshal = %s", encoded)
	}
	var system Event
	if err := json.Unmarshal(encoded, &system); err != nil {
		t.Fatal(err)
	}
	if system.IsWorkflowLog() || system.Provenance != LogProvenanceSystem {
		t.Fatalf("system event = %#v", system)
	}
}

func TestCanonicalLogProvenance(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{in: "workflow", want: LogProvenanceWorkflow},
		{in: "WORKFLOW", want: LogProvenanceWorkflow},
		{in: " workflow ", want: LogProvenanceWorkflow},
		{in: "system", want: LogProvenanceSystem},
		{in: "internal", want: LogProvenanceSystem},
		{in: "", want: LogProvenanceSystem},
		{in: "other", want: LogProvenanceSystem},
	} {
		if got := CanonicalLogProvenance(tc.in); got != tc.want {
			t.Fatalf("CanonicalLogProvenance(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
