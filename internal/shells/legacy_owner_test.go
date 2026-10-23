package shells

import (
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyDormantOwnerStaysOpen(t *testing.T) {
	root := t.TempDir()
	id := "ses_20261019-000001-aaaaaaaa"
	recordtest.Write(t, filepath.Join(root, "sessions"), id, map[string]any{"id": id, "finished": time.Now(), "status": "dormant"})
	if err := sessionlife.Check(filepath.Join(root, "sessions"), id); err != nil {
		t.Fatal(err)
	}
	s := &Store{Root: filepath.Join(root, "shells")}
	closed, err := s.ownerClosed(id)
	if err != nil || closed {
		t.Fatalf("legacy dormant owner is closed: %v, %v", closed, err)
	}
}
