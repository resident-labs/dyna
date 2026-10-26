package state

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestLandingMigrationArchivesOpaqueEvidenceOnce(t *testing.T) {
	root := newRoot(t)
	dataDir := filepath.Dir(root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pr_state(id,revision,body) VALUES(1,41,'{"version":1}')`); err != nil {
		t.Fatal(err)
	}
	const at = "2026-10-20T10:00:00Z"
	rows := map[string]string{
		"merged": `{"key":"merged","state":"merged","landing":{"state":"landed","at":"` + at + `","futureLanding":{"n":9007199254740993}},"reviewHistory":[{"head":"old"}]}`,
		"null":   `{"key":"null","state":"open","landing":null}`,
		"none":   `{"key":"none","state":"open"}`,
	}
	for k, rec := range rows {
		if _, err := db.Exec(`INSERT INTO pr_records(key,room,repo,number,closed,rev,body) VALUES(?,'room','repo',1,?,41,?)`, k, k == "merged", `{"record":`+rec+`}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	check := func(key, want string, rev int64) {
		t.Helper()
		var got string
		var r int64
		if err := db.QueryRow(`SELECT json_extract(?,'$.record'),rev FROM pr_records WHERE key=?`, joinedBody(t, db, key), key).Scan(&got, &r); err != nil {
			t.Fatal(err)
		}
		if got != want || r != rev {
			t.Fatalf("%s: rev=%d body=%s", key, r, got)
		}
	}
	merged := `{"closedAt":"` + at + `","key":"merged","reviewHistory":[{"head":"old"},{"state":"landed","at":"` + at + `","futureLanding":{"n":9007199254740993}}],"state":"merged"}`
	var rev int64
	db.QueryRow("SELECT revision FROM pr_state WHERE id=1").Scan(&rev)
	check("merged", merged, rev)
	check("null", `{"key":"null","state":"open"}`, rev)
	check("none", rows["none"], 41)
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db = mustOpen(t, root)
	check("merged", merged, rev)
}
