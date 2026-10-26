package state

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigrationPersistsClosedTimeWithoutLosingEvidence(t *testing.T) {
	root := newRoot(t)
	dataDir := filepath.Dir(root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pr_state(id,revision,body) VALUES(1,41,'{"version":1,"futureIndex":17}')`); err != nil {
		t.Fatal(err)
	}
	const at = "2026-10-20T10:00:00Z"
	const checked = "2026-10-21T10:00:00Z"
	cases := []struct {
		key, fields, want string
	}{
		{"landed", `"state":"merged","landing":{"state":"landed","at":"` + at + `","futureLanding":11},"checked":"` + checked + `"`, at},
		{"checked", `"state":"closed","checked":"` + checked + `","created":"` + at + `"`, checked},
		{"created", `"state":"closed","created":"` + at + `"`, at},
		{"explicit", `"state":"merged","closedAt":"` + at + `","checked":"` + checked + `"`, at},
		{"open", `"state":"open","checked":"` + checked + `"`, ""},
	}
	before := map[string]map[string]any{}
	for _, c := range cases {
		body := `{"futureRow":{"large":9007199254740993},"responses":{"pull":{"etag":"tag","body":{"future":true}}},"record":{"key":"` + c.key + `","futureRecord":19,` + c.fields + `}}`
		if _, err := db.Exec(`INSERT INTO pr_records(key,room,repo,number,closed,rev,body) VALUES(?,'room','repo',1,?,41,?)`, c.key, c.key != "open", body); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		before[c.key] = doc
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM pr_state WHERE id=1").Scan(&revision); err != nil || revision <= 41 {
		t.Fatalf("store revision=%d, err=%v; want newer than 41", revision, err)
	}
	bodies := map[string]string{}
	revisions := map[string]int64{}
	for _, c := range cases {
		var body string
		var rev int64
		if err := db.QueryRow("SELECT rev FROM pr_records WHERE key=?", c.key).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		body = joinedBody(t, db, c.key)
		var doc map[string]any
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		want := before[c.key]
		if c.want != "" {
			want["record"].(map[string]any)["closedAt"] = c.want
		}
		if rec := want["record"].(map[string]any); rec["landing"] != nil {
			// The landing migration archives it after the closure time is set.
			rec["reviewHistory"] = []any{rec["landing"]}
			delete(rec, "landing")
		}
		if !reflect.DeepEqual(doc, want) {
			t.Fatalf("%s lost evidence or closure time: %s", c.key, body)
		}
		changed := c.key != "explicit" && c.key != "open"
		wantRev := revision
		if c.key != "landed" {
			// Only the landing migration, which runs after this one, touches the landed row again.
			wantRev--
		}
		if changed && rev != wantRev || !changed && rev != 41 {
			t.Fatalf("%s revision=%d, store=%d", c.key, rev, revision)
		}
		var raw struct{ FutureRow json.RawMessage }
		if err := json.Unmarshal([]byte(body), &raw); err != nil || string(raw.FutureRow) != `{"large":9007199254740993}` {
			t.Fatalf("opaque number changed: %s, %v", body, err)
		}
		bodies[c.key], revisions[c.key] = body, rev
	}
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db = mustOpen(t, root)
	for _, c := range cases {
		var rev int64
		err := db.QueryRow("SELECT rev FROM pr_records WHERE key=?", c.key).Scan(&rev)
		if body := joinedBody(t, db, c.key); err != nil || body != bodies[c.key] || rev != revisions[c.key] {
			t.Fatalf("%s changed on repeat: rev=%d, err=%v", c.key, rev, err)
		}
	}
	var after int64
	if err := db.QueryRow("SELECT revision FROM pr_state WHERE id=1").Scan(&after); err != nil || after != revision {
		t.Fatalf("repeat store revision=%d, want %d: %v", after, revision, err)
	}
}
