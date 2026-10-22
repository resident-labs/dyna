package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bigState(n int) State {
	st := State{Version: 1, Records: map[string]*Record{}}
	for i := range n {
		key := fmt.Sprintf("github.com/acme/widgets#%d", i)
		st.Records[key] = &Record{Key: key, Repo: "acme/widgets", Number: i, RoomID: "room", State: "open", Watching: true}
	}
	return st
}

func changedRows(t *testing.T, root string) int {
	t.Helper()
	revision, err := Revision(root)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := mustOpen(t, root).QueryRow("SELECT count(*) FROM pr_records WHERE rev=?", revision).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWriteStoresOnlyChangedRows(t *testing.T) {
	root := newRoot(t)
	st := bigState(1025)
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if n := changedRows(t, root); n != 1025 {
		t.Fatalf("first write stored %d rows", n)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	st.Records["github.com/acme/widgets#7"].Title = "changed"
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if n := changedRows(t, root); n != 1 {
		t.Fatalf("one record change stored %d rows", n)
	}
	// An index-level change stores no record rows.
	st.MergePauses = map[string]string{"room": "paused"}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if n := changedRows(t, root); n != 0 {
		t.Fatalf("index change stored %d rows", n)
	}
	got, err := Read(root)
	if err != nil || got.Records["github.com/acme/widgets#7"].Title != "changed" || got.MergePauses["room"] != "paused" {
		t.Fatalf("read back: %v", err)
	}
}

// Each record's quota display is derived from the store's rate limits, and
// it is not stored. A new quota alone, which every GitHub reply brings, must
// store no record row; on the live store it rewrote every record of the
// account, hundreds of rows a minute.
func TestQuotaChangeStoresNoRecordRow(t *testing.T) {
	root := newRoot(t)
	st := bigState(50)
	for _, r := range st.Records {
		r.Host, r.QuotaAccount = "github.com", "octo"
	}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	remaining := 4321
	st.RateLimits[(RateLimitScope{"github.com", "octo", "core"}).Key()] = RateLimit{Remaining: &remaining, Reset: time.Now().Add(time.Hour)}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if n := changedRows(t, root); n != 0 {
		t.Fatalf("a quota change stored %d record rows, want 0", n)
	}
	got, err := Read(root)
	if err != nil || got.Records["github.com/acme/widgets#7"].RateLimit.Remaining == nil || *got.Records["github.com/acme/widgets#7"].RateLimit.Remaining != remaining {
		t.Fatalf("read back the quota display: %+v, %v", got.Records["github.com/acme/widgets#7"].RateLimit, err)
	}
}

func TestUnchangedWriteKeepsRevision(t *testing.T) {
	root := newRoot(t)
	st := bigState(3)
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	before, _ := Revision(root)
	st, _ = Read(root)
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if after, _ := Revision(root); after != before {
		t.Fatalf("revision %s -> %s without a change", before, after)
	}
}

func TestWriteDeletesRemovedRecord(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(3)); err != nil {
		t.Fatal(err)
	}
	st, _ := Read(root)
	delete(st.Records, "github.com/acme/widgets#1")
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	got, err := Read(root)
	if err != nil || len(got.Records) != 2 || got.Records["github.com/acme/widgets#1"] != nil {
		t.Fatalf("records after delete: %d, %v", len(got.Records), err)
	}
}

func TestReadSeesAnotherProcessWrite(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err != nil {
		t.Fatal(err)
	}
	// Another process stores a record change and a new revision.
	db := mustOpen(t, root)
	key := "github.com/acme/widgets#2"
	var body string
	if err := db.QueryRow("SELECT body FROM pr_records WHERE key=?", key).Scan(&body); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, `"title":""`, `"title":"from elsewhere"`, 1)
	if _, err := db.Exec("UPDATE pr_records SET body=?, rev=10 WHERE key=?", body, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE pr_state SET revision=10"); err != nil {
		t.Fatal(err)
	}
	got, err := Read(root)
	if err != nil || got.Records[key].Title != "from elsewhere" {
		t.Fatalf("stale read: %+v, %v", got.Records[key], err)
	}
	if revision, _ := Revision(root); revision != "10" {
		t.Fatalf("revision = %s", revision)
	}
}

func TestWritePrunesOldClosedRecords(t *testing.T) {
	root := newRoot(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	old := time.Now().Add(-15 * 24 * time.Hour)
	st := State{Version: 1, Records: map[string]*Record{
		"old":  {Key: "old", Number: 7, State: "merged", ClosedAt: old},
		"open": {Key: "open", State: "open"},
	}}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	got, _ := Read(root)
	if len(got.Records) != 1 || got.Records["open"] == nil {
		t.Fatalf("records: %v", got.Records)
	}
	// The prune says what it removed, once.
	want := "PR state: pruned merged record old (#7), closed " + old.Format(time.DateOnly)
	if strings.Count(logged.String(), want) != 1 {
		t.Fatalf("log = %q, want one %q", logged.String(), want)
	}
	if err := Write(root, got); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logged.String(), "pruned") != 1 {
		t.Fatalf("a later write logged again: %q", logged.String())
	}
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		fmt.Fprintf(h, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestImportSmallJSONStore(t *testing.T) {
	root := newRoot(t)
	if err := os.CopyFS(root, os.DirFS("testdata/prs")); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, root)
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if after := treeDigest(t, root); after != before {
		t.Fatal("import changed the JSON files")
	}
	if len(st.Records) != 3 {
		t.Fatalf("imported %d records", len(st.Records))
	}
	r := st.Records["github.com/acme/widgets#17"]
	if r == nil || r.Title != "Add knobs" || r.RoomID != "room-a" || r.Round != 2 || r.Verdict != "changes" ||
		len(r.Findings) != 1 || r.Findings[0].File != "knob.go" || len(r.Checks) != 1 || r.Polling.IntervalMinutes != 5 {
		t.Fatalf("record 17: %+v", r)
	}
	if string(st.Responses["github.com/acme/widgets#17"]["/repos/acme/widgets/pulls/17"].Body) != `{"number":17}` {
		t.Fatalf("responses: %+v", st.Responses)
	}
	if m := st.Records["github.com/acme/widgets#9"]; m == nil || m.State != "merged" || m.ClosedAt.IsZero() {
		t.Fatalf("closed record: %+v", m)
	}
	if st.Reviewers["room-a"].Profile != "judge" || st.CommentSessions["issue:1"] != "session-a" || len(st.RateLimits) != 1 {
		t.Fatalf("index fields: %+v", st)
	}
	var closed, rooms int
	db := mustOpen(t, root)
	if err := db.QueryRow("SELECT sum(closed), count(distinct room) FROM pr_records").Scan(&closed, &rooms); err != nil || closed != 1 || rooms != 2 {
		t.Fatalf("columns: closed=%d rooms=%d %v", closed, rooms, err)
	}
	// The import runs once; a later JSON change is not read again.
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	if again, err := Read(root); err != nil || len(again.Records) != 3 {
		t.Fatalf("second read: %d records, %v", len(again.Records), err)
	}
}

// A deferred foreign key makes COMMIT fail after every statement succeeded.
func TestFailedCommitLeavesDurableStateForReadAndRetry(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	// One connection keeps the foreign key pragma for the writer.
	db := mustOpen(t, root)
	db.SetMaxOpenConns(1)
	defer db.SetMaxOpenConns(0)
	for _, q := range []string{
		"PRAGMA foreign_keys=ON",
		"CREATE TABLE fk_parent(id INTEGER PRIMARY KEY)",
		"CREATE TABLE fk_child(p INTEGER REFERENCES fk_parent(id) DEFERRABLE INITIALLY DEFERRED)",
		"CREATE TRIGGER fail_commit AFTER UPDATE ON pr_state BEGIN INSERT INTO fk_child VALUES(1); END",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	key := "github.com/acme/widgets#1"
	st.Records[key].Title = "lost"
	if err := Write(root, st); err == nil {
		t.Fatal("commit did not fail")
	}
	got, err := Read(root)
	if err != nil || got.Records[key].Title != "" {
		t.Fatalf("read returned rolled-back state: %q, %v", got.Records[key].Title, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_commit"); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	if got, err = Read(root); err != nil || got.Records[key].Title != "lost" {
		t.Fatalf("retry skipped the change: %q, %v", got.Records[key].Title, err)
	}
}
