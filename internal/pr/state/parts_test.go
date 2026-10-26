package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

func partsState() (State, string) {
	const key = "github.com/acme/widgets#1"
	st := State{Version: 1, Records: map[string]*Record{key: {
		Key: key, Repo: "acme/widgets", Number: 1, RoomID: "room", State: "open", Watching: true,
		Notices: []Notice{{CommentID: "c1"}},
		Summary: "reviewed", HeldComments: map[string]HeldComment{"c": {}},
	}}, Responses: map[string]map[string]CachedResponse{key: {"pull": {ETag: "tag"}}}}
	return st, key
}

func partRows(t *testing.T, root string) map[string]string {
	t.Helper()
	rows, err := mustOpen(t, root).Query("SELECT part, body FROM pr_record_parts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var part, body string
		if err := rows.Scan(&part, &body); err != nil {
			t.Fatal(err)
		}
		out[part] = body
	}
	return out
}

// A watcher poll changes the checked time and nothing cold. It must rewrite
// the small hot row and leave the notices, review detail and responses alone.
func TestPollWriteRewritesOnlyTheHotRow(t *testing.T) {
	root := newRoot(t)
	st, key := partsState()
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	if got := partRows(t, root); len(got) != 3 {
		t.Fatalf("parts after the first write = %v, want notices, detail and responses", got)
	}
	// Change a part row behind the store's back. A poll must not rewrite it.
	if _, err := mustOpen(t, root).Exec(`UPDATE pr_record_parts SET body=json_set(body,'$.marker',1)`); err != nil {
		t.Fatal(err)
	}
	st.Records[key].Checked = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	for part, body := range partRows(t, root) {
		if !strings.Contains(body, `"marker":1`) {
			t.Fatalf("poll write rewrote part %s: %s", part, body)
		}
	}
	var hot int
	if err := mustOpen(t, root).QueryRow("SELECT length(body) FROM pr_records WHERE key=?", key).Scan(&hot); err != nil || hot == 0 {
		t.Fatalf("hot row: %d, %v", hot, err)
	}
}

// Reading joins the hot row and its parts back into the record, and a part
// that becomes empty is removed.
func TestRecordsRoundTripThroughPartsAndDropEmptyParts(t *testing.T) {
	root := newRoot(t)
	st, key := partsState()
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Records[key]
	if len(r.Notices) != 1 || r.Summary != "reviewed" || len(r.HeldComments) != 1 || got.Responses[key]["pull"].ETag != "tag" {
		t.Fatalf("round trip lost cold fields: %+v responses %v", r, got.Responses[key])
	}
	got.Records[key].Notices = nil
	if err := Write(root, got); err != nil {
		t.Fatal(err)
	}
	if _, kept := partRows(t, root)["notices"]; kept {
		t.Fatal("an empty notices part stayed in the store")
	}
}

// The migration rewrites every whole-body row once: cold fields move to part
// rows, unknown fields stay in the hot body, and Read still returns the record.
func TestMigrationMovesColdFieldsOutOfExistingRows(t *testing.T) {
	root := newRoot(t)
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(filepath.Dir(root)) })
	err = appstate.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), schema); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), `INSERT INTO pr_state VALUES(1,5,'{"version":1}');
INSERT INTO pr_records VALUES('k','room','acme/widgets',1,0,5,'{"record":{"key":"k","state":"open","summary":"s","notifications":[{"commentId":"c1"}],"futureRecord":6},"responses":{"pull":{"etag":"t"}},"futureEnvelope":7}')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	var hot string
	if err := db.QueryRow("SELECT body FROM pr_records WHERE key='k'").Scan(&hot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hot, "notifications") || strings.Contains(hot, "responses") || !strings.Contains(hot, `"futureRecord":6`) || !strings.Contains(hot, `"futureEnvelope":7`) {
		t.Fatalf("hot body = %s", hot)
	}
	got, err := Read(root)
	if err != nil || len(got.Records["k"].Notices) != 1 || got.Records["k"].Summary != "s" || got.Responses["k"]["pull"].ETag != "t" {
		t.Fatalf("migrated record = %+v, %v", got.Records["k"], err)
	}
}

// A pre-split binary must not read half a record or write cold fields back
// into the hot row. It is refused at its version check; one that skipped the
// check (a process started before the migration) fails at the write.
func TestAPreSplitBinaryIsRefusedAfterTheSplit(t *testing.T) {
	root := newRoot(t)
	st, _ := partsState()
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, root)
	err := database.Migrate(db, "pr.state", 1, func(*sql.Conn, int) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "pr.state") {
		t.Fatalf("a binary that needs pr.state 1 got %v; want a newer-schema refusal", err)
	}
	// What a pre-split writer sends: the whole record in one body.
	whole := `{"record":{"key":"k","reviewRounds":null,"notifications":[{"id":"n"}]}}`
	for name, statement := range map[string]string{
		"insert": `INSERT INTO pr_records(key,room,repo,number,closed,rev,body) VALUES('old','room','r',2,0,9,?)`,
		"update": `UPDATE pr_records SET body=?`,
	} {
		if _, err := db.Exec(statement, whole); err == nil || !strings.Contains(err.Error(), "too old") {
			t.Fatalf("%s of a whole-body record: %v; want it refused", name, err)
		}
	}
}
