package state

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

// newRoot returns an empty PR folder inside its own data folder.
func newRoot(t *testing.T) string {
	t.Helper()
	root := Root(t.TempDir())
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(filepath.Dir(root)); err != nil {
			t.Error(err)
		}
	})
	return root
}

func mustOpen(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := open(root)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// forgetSnapshot makes the next read behave like a new process.
func forgetSnapshot() {
	snapshot.Lock()
	snapshot.root, snapshot.index = "", nil
	snapshot.Unlock()
}

func TestReadMissingStoreIsEmptyAndHasNoRevision(t *testing.T) {
	root := Root(filepath.Join(t.TempDir(), "absent"))
	st, err := Read(root)
	if err != nil || st.Version != 1 || st.Records == nil || st.Reviewers == nil || st.NoiseRules == nil {
		t.Fatalf("empty snapshot: %+v, %v", st, err)
	}
	if revision, err := Revision(root); err != nil || revision != "missing" {
		t.Fatalf("revision = %q, %v", revision, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("reader created the JSON folder: %v", err)
	}
}

func TestImportValidatesJSONStore(t *testing.T) {
	for _, tc := range []struct{ name, data, wantError string }{
		{"invalid JSON", `{`, "invalid PR state"},
		{"legacy missing version", `{}`, ""},
		{"zero version", `{"version":0}`, "unsupported PR state version 0"},
		{"version-2 fence", `{"version":2}`, ""},
		{"future version", `{"version":4}`, "unsupported PR state version 4"},
		{"null maps", `{"version":1,"records":null,"reviewers":null,"noiseRules":null}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			st, err := Read(root)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
			} else if err != nil || st.Records == nil || st.Reviewers == nil || st.NoiseRules == nil {
				t.Fatalf("normalized snapshot: %+v, %v", st, err)
			}
		})
	}
}

// A stored row whose body holds no record crashed every PR watcher loop
// that read r.Watching. A read leaves it out, so no caller meets a nil record.
func TestReadLeavesOutARowWithoutARecord(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := mustOpen(t, root).Exec(`UPDATE pr_rows SET body='{"record":null}' WHERE key=?`, "github.com/acme/widgets#1"); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 1 || st.Records["github.com/acme/widgets#0"] == nil {
		t.Fatalf("records = %v, want only #0", st.Records)
	}
	if _, kept := st.Records["github.com/acme/widgets#1"]; kept {
		t.Fatal("a row without a record was read as a nil record")
	}
	st.Records["github.com/acme/widgets#0"].Title = "changed"
	if err := Write(root, st); err != nil {
		t.Fatalf("write after a row without a record: %v", err)
	}
}
