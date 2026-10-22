package state

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/changepolicy"
)

func TestSnapshotMetadataIsIsolatedAndOmitsRecordHistory(t *testing.T) {
	st := cloneFixture(2)
	st.Policies = map[string]changepolicy.Policy{"room": {RequiredChecks: []string{"go"}}}
	snap := Snapshot{state: st, revision: "1"}
	before, err := json.Marshal(snap.state)
	if err != nil {
		t.Fatal(err)
	}
	metadata := snap.Metadata()
	if metadata.Records != nil || metadata.Responses != nil || metadata.Version != 1 || metadata.Policies["room"].RequiredChecks[0] != "go" {
		t.Fatalf("metadata copied PR history or lost policy: %+v", metadata)
	}
	metadata.Reviewers["room"].FailedProfiles["p"] = time.Unix(1, 0)
	metadata.Policies["room"].RequiredChecks[0] = "changed"
	metadata.MergeTrials["t"].Changes[0].Key = "changed"
	after, err := json.Marshal(snap.state)
	if err != nil || string(after) != string(before) || snap.Revision() != "1" {
		t.Fatalf("metadata mutation changed the committed snapshot: %v", err)
	}
	if record, ok := snap.Record("pr"); !ok || len(record.Checks) != 2 {
		t.Fatal("metadata read changed record lookup")
	}
}

func TestSnapshotResponsesAreIsolated(t *testing.T) {
	st := cloneFixture(2)
	var key string
	for k := range st.Records {
		key = k
		break
	}
	st.Responses = map[string]map[string]CachedResponse{key: {"jobs": {Body: []byte("{}")}}}
	snap := Snapshot{state: st, revision: "1"}
	responses := snap.Responses(key)
	responses["jobs"].Body[0] = '['
	delete(responses, "jobs")
	if got := snap.Responses(key); string(got["jobs"].Body) != "{}" {
		t.Fatalf("response mutation reached the retained snapshot: %+v", got)
	}
	if got := snap.Responses("missing"); got != nil {
		t.Fatalf("missing record responses = %+v", got)
	}
}
