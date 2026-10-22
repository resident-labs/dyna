package view

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/listchange"
)

// PRs with failed or running CI are polled every minute. Each poll moved only
// Checked, and republishing all those rows made every open app reload its PRs.
func TestChangeRowsPublishOnlyContentChanges(t *testing.T) {
	signatures := map[string]string{}
	row := ChangeView{Key: "pr", CI: "failed", Checked: time.Unix(100, 0).UTC()}
	sync := func(row ChangeView) []listchange.Event {
		t.Helper()
		events, err := listchange.Sync(signatures, publishedRows("changes", "room", []ChangeView{row}, map[string]bool{"pr": true}), false)
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	sync(row)
	row.Checked = time.Unix(160, 0).UTC()
	if events := sync(row); len(events) != 0 {
		t.Fatalf("a new poll time alone published %d events", len(events))
	}
	row.CI = "pending"
	events := sync(row)
	if len(events) != 1 {
		t.Fatalf("a CI change published %d events, want 1", len(events))
	}
	var got ChangeView
	if err := json.Unmarshal(events[0].Change.Item, &got); err != nil || got.CI != "pending" || !got.Checked.Equal(row.Checked) {
		t.Fatalf("published row %+v (%v), want the new CI and the current poll time", got, err)
	}
	if events := sync(row); len(events) != 0 {
		t.Fatalf("an unchanged row published %d events", len(events))
	}
}
