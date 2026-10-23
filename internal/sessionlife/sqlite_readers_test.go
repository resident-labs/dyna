package sessionlife

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"dyna-agent/internal/database"
)

func TestListingsDoNotResurrectImportedSessions(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { _ = database.Close(data) })
	sessions := filepath.Join(data, "sessions")
	source := filepath.Join(sessions, "ses_deleted", legacyName)
	body := `{"id":"ses_deleted"}`
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	if err := Delete(sessions, "ses_deleted"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sessions, "ses_stray"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(data, "missing"), filepath.Join(sessions, "ses_link")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ses_open", "ses_closed", "ses_history"} {
		record := map[string]any{"id": id}
		if id == "ses_closed" {
			record["closed"] = closedAt
		}
		if id == "ses_history" {
			record["historyOnly"] = true
		}
		if err := Save(sessions, id, record); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{"ses_closed", "ses_history", "ses_open"}
	open := []string{"ses_history", "ses_open"}
	for _, listing := range []struct {
		name string
		read func(string) (map[string]Version, error)
		want []string
	}{{"Versions", Versions, all}, {"OpenVersions", OpenVersions, open}} {
		versions, err := listing.read(sessions)
		var ids []string
		for id, version := range versions {
			ids = append(ids, id)
			if version == "" {
				t.Errorf("%s: empty version for %s", listing.name, id)
			}
		}
		sort.Strings(ids)
		if err != nil || !reflect.DeepEqual(ids, listing.want) {
			t.Errorf("%s = %v, %v; want %v", listing.name, ids, err, listing.want)
		}
	}
	for _, read := range []func(string) ([]string, error){IDs, SafeIDs} {
		if ids, err := read(sessions); err != nil || !reflect.DeepEqual(ids, all) {
			t.Errorf("IDs = %v, %v; want %v", ids, err, all)
		}
	}
	for _, onlyOpen := range []bool{false, true} {
		var ids []string
		err := Each(sessions, onlyOpen, func(id string, _ []byte, err error) error {
			ids = append(ids, id)
			return err
		})
		want := all
		if onlyOpen {
			want = open
		}
		if err != nil || !reflect.DeepEqual(ids, want) {
			t.Errorf("Each(open=%v) = %v, %v; want %v", onlyOpen, ids, err, want)
		}
	}
	if raw, err := os.ReadFile(source); err != nil || string(raw) != body {
		t.Fatalf("retained source changed: %q, %v", raw, err)
	}
}
