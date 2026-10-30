package goals

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestGoalJSONNilLists(t *testing.T) {
	got, err := json.Marshal(Goal{})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{[]byte(`"holders":null`), []byte(`"edits":null`)} {
		if !bytes.Contains(got, part) {
			t.Errorf("missing %s in %s", part, got)
		}
	}
	if bytes.Contains(got, []byte(`"amendments":`)) {
		t.Errorf("empty amendments must be omitted: %s", got)
	}
}
