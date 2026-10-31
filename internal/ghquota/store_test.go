package ghquota

import (
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
)

func TestStorePersistsEndpointAndSourceCounts(t *testing.T) {
	dir := t.TempDir()
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := engineGHQuota
	engineGHQuota = &quotaLog{}
	t.Cleanup(func() { engineGHQuota = old })
	defer UseStore(state.For(db))()
	Record("repos/private/repo/pulls/123", false)
	Record("repos/private/repo/pulls/123", true)
	RecordCaller("access-check", false)
	rows, err := db.Query("SELECT kind,label,counted,free FROM ghquota_counts ORDER BY kind")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		kind, label   string
		counted, free int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.label, &r.counted, &r.free); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (row{"endpoint", "pull", 1, 1}) || got[1] != (row{"source", "access-check", 1, 0}) {
		t.Fatalf("counts = %+v", got)
	}
}
