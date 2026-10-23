package sessionlife

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const closedAt = "2026-10-01T00:00:00Z"

// The listing queries read only the covering index, never the wide meta rows.
func TestListingQueriesReadOnlyTheCoveringIndex(t *testing.T) {
	sessions := t.TempDir()
	if err := Save(sessions, "ses_a", map[string]any{"id": "ses_a"}); err != nil {
		t.Fatal(err)
	}
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{versionsQuery, openQuery} {
		var plan []string
		rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		got := strings.Join(plan, "; ")
		if !strings.Contains(got, "COVERING INDEX session_records_closed") {
			t.Errorf("%s: plan %q reads the table", query, got)
		}
		// The open listing seeks past closed rows instead of scanning them.
		if query == openQuery && !strings.Contains(got, "SEARCH session_records USING COVERING INDEX session_records_closed (closed=?)") {
			t.Errorf("%s: plan %q scans closed rows", query, got)
		}
	}
}

// OpenVersions lists only open rows and sees each new row at once.
func TestOpenVersionsListsOnlyOpenRows(t *testing.T) {
	sessions := t.TempDir()
	save := func(id string, closed bool) {
		t.Helper()
		record := map[string]any{"id": id}
		if closed {
			record["closed"] = closedAt
		}
		if err := Save(sessions, id, record); err != nil {
			t.Fatal(err)
		}
	}
	folder := func(id string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	open := func() map[string]bool {
		t.Helper()
		got, err := OpenVersions(sessions)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for id := range got {
			ids[id] = true
		}
		return ids
	}
	save("ses_open", false)
	save("ses_closed", true)
	folder("ses_open")
	folder("ses_closed")
	folder("ses_legacy")
	if got := open(); len(got) != 1 || !got["ses_open"] {
		t.Fatalf("OpenVersions = %v, want ses_open", got)
	}
	save("ses_new", false)
	if got := open(); !got["ses_new"] {
		t.Fatalf("a new row is missing: %v", got)
	}
	save("ses_legacy", true)
	if got := open(); got["ses_legacy"] {
		t.Fatalf("a folder whose record closed is still listed: %v", got)
	}
}
