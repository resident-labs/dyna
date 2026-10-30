package gitsvc

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFileContractJSON(t *testing.T) {
	value := struct {
		History GitHistoryCommit `json:"history"`
		Status  GitStatusResult  `json:"status"`
	}{History: GitHistoryCommit{GitCommit: GitCommit{Hash: "abc"}}}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{[]byte(`"hash":"abc"`), []byte(`"files":null`), []byte(`"commits":null`)} {
		if !bytes.Contains(got, part) {
			t.Errorf("missing %s in %s", part, got)
		}
	}
	if bytes.Contains(got, []byte(`"GitCommit":`)) {
		t.Errorf("embedded commit not flattened: %s", got)
	}
}
