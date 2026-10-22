package plan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/plan"
	"dyna-agent/internal/planclient"
)

// These golden bytes pin the immutable graph format, including struct field
// order, escaping and the final newline. Rollback copies preserve graph values.
func TestStorageGoldenBytes(t *testing.T) {
	testAncestryEngine(t)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := &plan.Plan{
		Version:          1,
		ExtractorVersion: "7",
		PlanHash:         "plan-hash-golden",
		SourcePath:       "wf.js",
		SourceSHA256:     "source-sha-golden",
		Status:           "complete",
		Diagnostics:      []plan.Diagnostic{{Level: "warning", Code: "stale_source", Message: "left in place"}},
		CreatedAt:        at,
		Nodes: []plan.Node{{
			PlanNodeID:     "n-1",
			WorkflowPlanID: "w-1",
			Kind:           "agent",
			Source:         plan.Span{StartByte: 1, EndByte: 2, StartLine: 1, EndLine: 1},
			Certainty:      "proven",
			Cardinality:    plan.Cardinality{Kind: "one"},
		}},
		Edges:  []plan.Edge{{From: "n-1", To: "n-2", Kind: "next"}},
		Counts: plan.Counts{Agents: 1, Workflows: 1, Nodes: 2, Edges: 1},
	}
	dir := t.TempDir()
	if err := plan.WriteAtomic(dir, p, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"version\": 1,\n  \"extractorVersion\": \"7\",\n  \"planHash\": \"plan-hash-golden\",\n  \"sourcePath\": \"wf.js\",\n  \"sourceSha256\": \"source-sha-golden\",\n  \"status\": \"complete\",\n  \"diagnostics\": [\n    {\n      \"level\": \"warning\",\n      \"code\": \"stale_source\",\n      \"message\": \"left in place\"\n    }\n  ],\n  \"createdAt\": \"2026-01-02T03:04:05Z\",\n  \"nodes\": [\n    {\n      \"planNodeId\": \"n-1\",\n      \"workflowPlanId\": \"w-1\",\n      \"kind\": \"agent\",\n      \"source\": {\n        \"startByte\": 1,\n        \"endByte\": 2,\n        \"startLine\": 1,\n        \"endLine\": 1\n      },\n      \"certainty\": \"proven\",\n      \"cardinality\": {\n        \"kind\": \"one\"\n      }\n    }\n  ],\n  \"edges\": [\n    {\n      \"from\": \"n-1\",\n      \"to\": \"n-2\",\n      \"kind\": \"next\"\n    }\n  ],\n  \"counts\": {\n    \"agents\": 1,\n    \"phases\": 0,\n    \"workflows\": 1,\n    \"nodes\": 2,\n    \"edges\": 1\n  }\n}\n"
	if string(got) != want {
		t.Fatalf("plan bytes = %q; want %q", got, want)
	}
	// Compare the old encoder directly as well as the literal golden.
	old, err := json.MarshalIndent(p, "", "  ")
	if err != nil || string(got) != string(append(old, '\n')) {
		t.Fatal("old plan encoder differs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plan mode = %o", info.Mode().Perm())
	}
	parent := "ses_20260101-000000-aaaaaaaa"
	if _, err := planclient.StampAncestry(t.Context(), dir, parent); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rollback plan.Plan
	if err := json.Unmarshal(raw, &rollback); err != nil {
		t.Fatal(err)
	}
	p.ResumedFrom = parent
	if !reflect.DeepEqual(p, &rollback) {
		t.Fatalf("rollback changed graph values: got %#v; want %#v", rollback, p)
	}
}
