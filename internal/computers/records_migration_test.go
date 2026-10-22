package computers

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestComputerMigrationKeepsCanonicalColumns(t *testing.T) {
	id := recordTestID(t)
	dir := filepath.Dir(Root())
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const ddl = `CREATE TABLE computers(session TEXT PRIMARY KEY, meta BLOB CHECK(meta IS NULL OR json_valid(meta)), status BLOB CHECK(status IS NULL OR json_valid(status))); INSERT INTO meta(key,value) VALUES('computers.records','1');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"session_id":"` + id + `","room":"canonical","unknown":"keep"}`)
	if _, err := db.Exec("INSERT INTO computers(session,meta) VALUES(?,?)", id, canonical); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaRecord.path(id), []byte("malformed stale metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	status := []byte(`{"session_id":"` + id + `","state":"running","unknown":"preserve"}`)
	if err := os.WriteFile(statusRecord.path(id), status, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	var gotMeta, gotStatus []byte
	if err := db.QueryRow("SELECT meta,status FROM computers WHERE session=?", id).Scan(&gotMeta, &gotStatus); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotMeta, canonical) || !bytes.Equal(gotStatus, status) {
		t.Fatalf("migration columns: meta=%q status=%q", gotMeta, gotStatus)
	}
	changed := []byte("retained file changed")
	for _, r := range []record{metaRecord, statusRecord} {
		if err := os.WriteFile(r.path(id), changed, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ReadMeta(id)
	if err != nil || m.Room != "canonical" {
		t.Fatalf("metadata: %+v, %v", m, err)
	}
	st := savedStatus(id)
	if st.State != StateRunning {
		t.Fatalf("status: %+v", st)
	}
	st.State = StateIdleStopped
	if err := saveStatus(st); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if got := savedStatus(id); got.State != StateIdleStopped {
		t.Fatalf("saved status: %+v", got)
	}
	if err := db.QueryRow("SELECT meta FROM computers WHERE session=?", id).Scan(&gotMeta); err != nil || !bytes.Equal(gotMeta, canonical) {
		t.Fatalf("metadata changed: %q, %v", gotMeta, err)
	}
	for _, r := range []record{metaRecord, statusRecord} {
		after, err := os.ReadFile(r.path(id))
		if err != nil || !bytes.Equal(after, changed) {
			t.Fatalf("source %s changed: %q, %v", r.file, after, err)
		}
	}
}
