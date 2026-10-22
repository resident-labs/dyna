package goals

import (
	"strings"
	"testing"
)

// TestScopedQueriesUseIndexes checks that each scoped goal query searches an
// index instead of scanning every goal row.
func TestScopedQueriesUseIndexes(t *testing.T) {
	s := testStore(t)
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	where, args := named("r", "g1")
	for _, q := range []struct {
		name, where string
		args        []any
	}{
		{"open goals", openGoals, nil},
		{"open goals of a room", "room=? AND " + openGoals, []any{"r"}},
		{"one room", "room=?", []any{"r"}},
		{"one goal", "room=? AND id=?", []any{"r", "g1"}},
		{"open goals and a named goal", where, args},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN SELECT room,body FROM goals WHERE "+q.where, q.args...)
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
		for _, step := range plan {
			if strings.HasPrefix(step, "SCAN goals") {
				t.Errorf("%s scans every goal: %v", q.name, plan)
			}
		}
	}
}
