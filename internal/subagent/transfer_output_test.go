package subagent

import (
	"os"
	"path/filepath"
	"testing"
)

// OpenCode prints no reasoning events, so a model that reasoned for a minute
// showed only step_start. The transfer check stopped two keeper turns at
// 60 seconds while mimo was still working.
func TestTransferCountsStepStartAsModelStart(t *testing.T) {
	m := &Manager{Store: &Store{Root: t.TempDir()}}
	id := "ses_20261018-000000-aaaaaaaa"
	path, err := m.Store.NormalizedPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(line string) {
		if err := os.WriteFile(path, []byte(line+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"v":1,"kind":"status","name":"session","state":"started"}`)
	if m.transferHasOutput(id, Turn{}) {
		t.Fatal("a session status counted as model output")
	}
	write(`{"v":1,"kind":"status","name":"step","state":"started"}`)
	if !m.transferHasOutput(id, Turn{}) {
		t.Fatal("step_start did not count as model start")
	}
}
