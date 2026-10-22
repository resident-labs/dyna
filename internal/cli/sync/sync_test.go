package sync

import (
	"bytes"
	"dyna-agent/internal/database"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusJSON(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if e := os.MkdirAll(store.DataDir(), 0700); e != nil {
		t.Fatal(e)
	}
	if e := store.WriteJSON(filepath.Join(store.DataDir(), "sync.json"), map[string]any{"repositories": map[string]any{"/repo": map[string]any{"origin": map[string]any{"ahead": 2, "behind": 3}}}}, 0600); e != nil {
		t.Fatal(e)
	}
	if err := lanesync.Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(store.DataDir()) })
	c := NewCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"status", "--json"})
	if e := c.Execute(); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"ahead": 2`) || !strings.Contains(out.String(), `"behind": 3`) {
		t.Fatal(out.String())
	}
}
