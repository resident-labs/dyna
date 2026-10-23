package update

import (
	"errors"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestRetainedCheckFileImportsOnce(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	checked := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	retained := `{"lastChecked":"` + checked.Format(time.RFC3339Nano) + `","etag":"\"release-1\"","release":{"tag_name":"v1.2.0"}}`
	if err := os.WriteFile(checkFilePath(dir), []byte(retained), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}

	cfg := Config{DataDir: dir}
	st, err := cfg.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Release.TagName != "v1.2.0" || st.ETag != `"release-1"` || !st.LastChecked.Equal(checked) {
		t.Fatalf("imported state = %+v", st)
	}

	// The file is a one-time source: later checks write dyna.db only.
	if err := cfg.saveState(checkState{LastChecked: checked.Add(time.Hour), Release: Release{TagName: "v1.3.0"}}); err != nil {
		t.Fatal(err)
	}
	if st, err = cfg.loadState(); err != nil || st.Release.TagName != "v1.3.0" {
		t.Fatalf("state after save = %+v, %v", st, err)
	}
	if raw, err := os.ReadFile(checkFilePath(dir)); err != nil || string(raw) != retained {
		t.Fatalf("retained file = %q, %v; want it unchanged", raw, err)
	}
}

func TestCheckStateRoundTripsInSQLite(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	cfg := Config{DataDir: dir}
	checked := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	if err := cfg.saveState(checkState{LastChecked: checked, ETag: `"release-2"`, Release: Release{TagName: "v2.0.0"}, LastError: "check failed"}); err != nil {
		t.Fatal(err)
	}
	st, err := cfg.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.LastChecked.Equal(checked) || st.ETag != `"release-2"` || st.Release.TagName != "v2.0.0" || st.LastError != "check failed" {
		t.Fatalf("state = %+v", st)
	}

	// A second save replaces the row instead of adding one.
	if err := cfg.saveState(checkState{LastChecked: checked.Add(time.Hour), Release: Release{TagName: "v2.1.0"}}); err != nil {
		t.Fatal(err)
	}
	if st, err = cfg.loadState(); err != nil || st.Release.TagName != "v2.1.0" || st.ETag != "" || st.LastError != "" {
		t.Fatalf("state after save = %+v, %v", st, err)
	}
	if _, err := os.Stat(checkFilePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check state wrote a file: %v", err)
	}
}
