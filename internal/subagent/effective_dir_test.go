package subagent

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

// A session whose own directory is gone, in a room without a repository,
// runs its next turn in the parent session's directory (D-397).
func TestEffectiveDirFallsBackToParentDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	parentDir := t.TempDir()
	parent := "ses_20261018-000001-aaaaaaaa"
	recordtest.Write(t, runstore.SessionsDir(), parent, map[string]any{
		"id": parent, "room": "dyna", "roomId": "room_1", "dir": parentDir,
	})
	manager := NewManager()
	meta := Meta{
		Dir:           filepath.Join(t.TempDir(), "removed"),
		RoomID:        "missing-room",
		ParentSession: parent,
	}
	dir, note := manager.EffectiveDir(meta)
	if dir != parentDir {
		t.Fatalf("turn dir = %q, want the parent's directory %q", dir, parentDir)
	}
	if !strings.Contains(note, "no longer exists") {
		t.Fatalf("note does not say the saved directory is gone: %q", note)
	}
}
