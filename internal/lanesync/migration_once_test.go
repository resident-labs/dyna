package lanesync

import (
	"bytes"
	"os"
	"testing"

	"dyna-agent/internal/database"
)

func TestLaneSyncUpgradePreservesCanonicalEmptyState(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	db, err := database.Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	const oldDDL = `CREATE TABLE lane_sync (id INTEGER PRIMARY KEY CHECK(id=1), snapshot BLOB NOT NULL, revision INTEGER NOT NULL, legacy_version TEXT NOT NULL DEFAULT ''); INSERT INTO lane_sync VALUES(1,'{}',0,''); INSERT INTO meta(key,value) VALUES('lanesync.schema','2');`
	if _, err := db.Exec(oldDDL); err != nil {
		t.Fatal(err)
	}
	source := []byte(`{"events":{"stale":"file"}}`)
	if err := os.WriteFile(s.path(), source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read()
	if err != nil || len(got.Events) != 0 {
		t.Fatalf("canonical empty state replaced: %+v, %v", got, err)
	}
	if err := s.Update(func(st *Snapshot) error { st.Events["current"] = "row"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	got, err = s.Read()
	if err != nil || got.Events["current"] != "row" || len(got.Events) != 1 {
		t.Fatalf("canonical update replaced: %+v, %v", got, err)
	}
	after, err := os.ReadFile(s.path())
	if err != nil || !bytes.Equal(after, source) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}
