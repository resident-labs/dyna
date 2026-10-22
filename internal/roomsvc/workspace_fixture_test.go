package roomsvc

import (
	"dyna-agent/internal/room"
	"os"
	"path/filepath"
	"testing"
)

func workspaceEnv(t *testing.T) (*env, room.Record, string) {
	t.Helper()
	e := newEnv(t)
	root := t.TempDir()
	rec, err := e.svc.rooms().Create(room.CreateInput{Name: "workspace", Repo: root, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return e, rec, root
}

func writeWorkspace(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
