package sessionlife

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A direct listing of the sessions folder, which IDs replaces: the runstore
// path anywhere, or the subagent store's root inside internal/subagent.
var (
	listsSessionsDir = regexp.MustCompile(`(ReadDir|os\.Open|OpenRoot)\(runstore\.SessionsDir\(\)\)`)
	listsStoreRoot   = regexp.MustCompile(`(ReadDir|os\.Open|OpenRoot)\((s|store|m\.store\(\))\.root\(\)\)`)
)

func readsSessionsFolder(rel string, src []byte) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	return listsSessionsDir.Match(src) || strings.HasPrefix(rel, filepath.Join("internal", "subagent")+string(filepath.Separator)) && listsStoreRoot.Match(src)
}

// Only the record code names session.json, so the record can move into
// dyna.db without touching its callers or their tests. Tests use recordtest.
func TestOnlyTheRecordCodeTouchesSessionJSON(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// One git grep finds the few candidate files; reading every Go file took
	// seconds on a loaded CI machine. --untracked also sees new files.
	out, err := exec.Command("git", "-C", repo, "grep", "-l", "--untracked", "-E",
		"-e", `"session\.json"`, "-e", listsSessionsDir.String(), "-e", listsStoreRoot.String(),
		"--", "internal/*.go", "cmd/*.go").Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) { // 1: no match
		t.Fatalf("git grep: %v", err)
	}
	var found []string
	for _, rel := range strings.Fields(string(out)) {
		// This one-time migration enumerates artifacts, without reading session records.
		if strings.HasPrefix(rel, "internal/sessionlife/") || rel == "internal/subagent/turn_migration.go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"session.json"`) || readsSessionsFolder(filepath.FromSlash(rel), b) {
			found = append(found, rel)
		}
	}
	if len(found) > 0 {
		t.Fatalf("these files name session.json or list the sessions folder; use the record API in internal/sessionlife (tests: internal/sessionlife/recordtest): %v", found)
	}
}

func TestLegacyLoadRefusesUnsafeOrInvalidRecords(t *testing.T) {
	sessions := t.TempDir()
	id := "ses_20261020-000000-abcdef12"
	dir := filepath.Join(sessions, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	write := func(data string, mode os.FileMode) {
		os.Remove(path)
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	var s Snapshot
	load := func() error {
		raw, _, err := legacyLoadRaw(sessions, id)
		if err != nil {
			return err
		}
		return decode(id, raw, &s)
	}
	if err := load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing record = %v, want os.ErrNotExist", err)
	}
	write(`{"id":"`+id+`","turns":2}`, 0o600)
	if err := load(); err != nil || s.Turns != 2 {
		t.Fatalf("Load = %+v, %v", s, err)
	}
	for name, bad := range map[string]func(){
		"public":     func() { write(`{}`, 0o644) },
		"not object": func() { write(`null`, 0o600) },
		"too large":  func() { write(`{"x":"`+strings.Repeat("a", recordLimit)+`"}`, 0o600) },
		"symlink": func() {
			other := filepath.Join(t.TempDir(), "other.json")
			if err := os.WriteFile(other, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			os.Remove(path)
			if err := os.Symlink(other, path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		bad()
		if err := load(); err == nil {
			t.Errorf("%s record loaded", name)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b"} {
		if err := Load(sessions, bad, &s); err == nil {
			t.Errorf("Load accepted session ID %q", bad)
		}
	}
}

func TestSaveThenLoad(t *testing.T) {
	sessions := t.TempDir()
	id := "ses_20261020-000000-abcdef12"
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, id, map[string]any{"id": id, "turns": 3}); err != nil {
		t.Fatal(err)
	}
	before, ok := RecordVersion(sessions, id)
	if !ok {
		t.Fatal("no version after Save")
	}
	var s Snapshot
	if err := Load(sessions, id, &s); err != nil || s.Turns != 3 {
		t.Fatalf("Load after Save = %+v, %v", s, err)
	}
	if err := Save(sessions, id, map[string]any{"id": id, "turns": 4}); err != nil {
		t.Fatal(err)
	}
	if after, _ := RecordVersion(sessions, id); after == before {
		t.Fatal("Save kept the record version")
	}
}

// Revision changes on every write, also when the newest record is deleted
// and the next write reuses its row revision.
func TestRevisionChangesAfterTheNewestRecordIsReplaced(t *testing.T) {
	sessions := t.TempDir()
	save := func(id string) {
		t.Helper()
		if err := Save(sessions, id, map[string]any{"id": id}); err != nil {
			t.Fatal(err)
		}
	}
	revision := func() string {
		t.Helper()
		rev, err := Revision(sessions)
		if err != nil || rev == "" {
			t.Fatalf("Revision = %q, %v", rev, err)
		}
		return rev
	}
	save("ses_20261020-000000-aaaaaaaa")
	save("ses_20261020-000000-bbbbbbbb")
	before := revision()
	if err := Delete(sessions, "ses_20261020-000000-bbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	save("ses_20261020-000000-cccccccc")
	if after := revision(); after == before {
		t.Fatalf("Revision stayed %q after the newest record was replaced", after)
	}
}

// A session folder replaced by a symlink to outside sessions must not be
// read, and the failure must not look like a missing record: callers treat
// a missing record as no evidence.
func TestLegacyLoadStaysInsideSessions(t *testing.T) {
	sessions := t.TempDir()
	id := "ses_20261020-000000-abcdef12"
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "session.json"), []byte(`{"id":"`+id+`","closed":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sessions, id)); err != nil {
		t.Fatal(err)
	}
	raw, _, err := legacyLoadRaw(sessions, id)
	if err == nil || errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrRecord) {
		t.Fatalf("Load through a symlinked session folder = %v, %+v; want ErrRecord", err, raw)
	}
	if _, ok := RecordVersion(sessions, id); ok {
		t.Fatal("RecordVersion followed a symlinked session folder")
	}
}

func TestIDsListsSessionFoldersAndRefusesSymlinks(t *testing.T) {
	sessions := t.TempDir()
	for _, name := range []string{"ses_b", "ses_a", "notes"} {
		if err := os.Mkdir(filepath.Join(sessions, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sessions, "ses_file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ids, err := IDs(sessions)
	if err != nil || strings.Join(ids, ",") != "notes,ses_a,ses_b" {
		t.Fatalf("IDs = %v, %v", ids, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(sessions, "ses_link")); err != nil {
		t.Fatal(err)
	}
	if ids, err := IDs(sessions); !errors.Is(err, ErrRecord) || strings.Join(ids, ",") != "notes,ses_a,ses_b" {
		t.Fatalf("IDs with a symlinked entry = %v, %v; want the safe IDs and ErrRecord", ids, err)
	}
	if ids, err := SafeIDs(sessions); err != nil || strings.Join(ids, ",") != "notes,ses_a,ses_b" {
		t.Fatalf("SafeIDs with a symlinked entry = %v, %v; want the safe IDs", ids, err)
	}
	if ids, err := IDs(filepath.Join(sessions, "missing")); err != nil || ids != nil {
		t.Fatalf("IDs of a missing folder = %v, %v", ids, err)
	}
}
