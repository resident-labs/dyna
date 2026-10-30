package view

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestGitHubDetailJSON(t *testing.T) {
	got, err := json.Marshal(GitHubDetail{GitHubItem: GitHubItem{Number: 42}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{[]byte(`"number":42`), []byte(`"labels":null`), []byte(`"comments":null`)} {
		if !bytes.Contains(got, part) {
			t.Errorf("missing %s in %s", part, got)
		}
	}
	if bytes.Contains(got, []byte(`"GitHubItem":`)) {
		t.Errorf("embedded item is not flattened: %s", got)
	}
}
