package view

import (
	"bytes"
	"encoding/json"
	"testing"

	"dyna-agent/internal/pr/state"
)

func TestChangeContractJSON(t *testing.T) {
	value := struct {
		Row  ChangeView    `json:"row"`
		Held HeldPRComment `json:"held"`
	}{Held: HeldPRComment{Key: "pr", HeldComment: state.HeldComment{ID: "comment"}}}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{[]byte(`"queuePosition":null`), []byte(`"botReviews":null`), []byte(`"checks":null`), []byte(`"key":"pr"`), []byte(`"id":"comment"`)} {
		if !bytes.Contains(got, part) {
			t.Errorf("missing %s in %s", part, got)
		}
	}
	if bytes.Contains(got, []byte(`"HeldComment":`)) {
		t.Errorf("embedded held comment not flattened: %s", got)
	}
}
