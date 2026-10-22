package view

import (
	"dyna-agent/internal/pr/watch"
	"encoding/json"
	"testing"
)

func TestDraftFactUsesTheSharedListRow(t *testing.T) {
	var record watch.Record
	if err := json.Unmarshal([]byte(`{"key":"repo#1","kind":"github","state":"open","isDraft":true}`), &record); err != nil {
		t.Fatal(err)
	}
	st := watch.State{Records: map[string]*watch.Record{record.Key: &record}}
	for _, row := range []ChangeView{sortedChanges(watch.ReviewFacts{State: st}, "", st.Records)[0], sortedChanges(watch.ReviewFacts{State: st}, "", st.Records)[0]} {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var facts struct {
			IsDraft bool `json:"isDraft"`
		}
		if err := json.Unmarshal(data, &facts); err != nil {
			t.Fatal(err)
		}
		if !facts.IsDraft {
			t.Fatalf("shared row lost draft fact: %s", data)
		}
	}
}
