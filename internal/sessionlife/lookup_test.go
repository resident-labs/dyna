package sessionlife

import (
	"strings"
	"testing"
	"time"
)

// A name lookup answers from one indexed query: it finds a bare name in every
// room, a full address in one room, and the generated name of an unnamed
// session, newest created first.
func TestLookupFindsNamesWithoutListingRecords(t *testing.T) {
	sessions := t.TempDir()
	unnamed := "ses_20261001-000000-abcdef12"
	for _, record := range []struct {
		id, room, name string
		created        time.Time
	}{
		{"ses_20261001-000001-11111111", "work", "lead", time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC)},
		{"ses_20261001-000002-22222222", "work", "lead", time.Date(2026, 10, 1, 0, 2, 0, 0, time.UTC)},
		{"ses_20261001-000003-33333333", "other", "lead", time.Date(2026, 10, 1, 0, 3, 0, 0, time.UTC)},
		{unnamed, "work", "", time.Date(2026, 10, 1, 0, 4, 0, 0, time.UTC)},
	} {
		saved := map[string]any{"id": record.id, "room": record.room, "roomId": "room_" + record.room, "created": record.created}
		if record.name != "" {
			saved["name"] = record.name
		}
		if err := Save(sessions, record.id, saved); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		what, room, name string
		want             []string
	}{
		{"an address", "work", "lead", []string{"ses_20261001-000002-22222222", "ses_20261001-000001-11111111"}},
		{"a bare name", "", "lead", []string{"ses_20261001-000003-33333333", "ses_20261001-000002-22222222", "ses_20261001-000001-11111111"}},
		{"a missing name", "", "nobody", nil},
		{"an empty name", "", "", nil},
		{"a generated name", "work", "agent-abcdef12", []string{unnamed}},
		{"a generated name in the wrong room", "other", "agent-abcdef12", nil},
	} {
		got, err := Lookup(sessions, tc.room, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: Lookup(%q, %q) = %v, want %v", tc.what, tc.room, tc.name, got, tc.want)
		}
	}
	db, err := records(sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []bool{false, true} {
		rows, err := db.Query("EXPLAIN QUERY PLAN "+lookupQuery(room), lookupArgs("work", "lead")...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
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
		if !strings.Contains(got, "session_records_name") || strings.Contains(got, "SCAN session_records") {
			t.Errorf("room=%t: plan %q does not seek the name index", room, got)
		}
	}
}
