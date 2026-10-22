package state

import (
	"encoding/json"
	"testing"
)

func TestMergeTrialReadsLegacyRootNotification(t *testing.T) {
	var b MergeTrial
	if err := json.Unmarshal([]byte(`{"rootNotified":true,"branch":"trial/1"}`), &b); err != nil {
		t.Fatal(err)
	}
	if !b.Notified || b.Notifier != "root" || b.Branch != "trial/1" {
		t.Fatalf("trial = %+v", b)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if _, old := saved["rootNotified"]; old {
		t.Fatal("legacy field was written")
	}
	if err := json.Unmarshal([]byte(`{"notified":true,"notifier":"steward"}`), &b); err != nil || b.Notifier != "steward" || !b.Notified {
		t.Fatalf("new trial = %+v, %v", b, err)
	}
}
