package ghquota

import (
	"slices"
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
	RecordRunPhase("repositories/987/actions/runs?head_sha=private", RunMergeTrial, false)
	rows, err := db.Query("SELECT kind,label,counted,free FROM ghquota_counts ORDER BY kind,label")
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
	want := []row{{"endpoint", "pull", 1, 1}, {"endpoint", "repository-id-runs-head-sha", 1, 0}, {"endpoint-phase", "pull/other", 1, 1}, {"endpoint-phase", "repository-id-runs-head-sha/merge-trial", 1, 0}, {"source", "access-check", 1, 0}}
	if !slices.Equal(got, want) {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}
