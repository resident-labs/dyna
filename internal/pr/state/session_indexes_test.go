package state

import (
	"fmt"
	"strings"
	"testing"
)

func TestFreshSessionQueryUsesIndexes(t *testing.T) {
	db := mustOpen(t, Root(t.TempDir()))
	rows, err := db.Query("EXPLAIN QUERY PLAN "+fmt.Sprintf(sessionsQuery, "?", "?", "?"), "room", "a", "room", "a", "room", "a")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"pr_rows_author_session", "pr_rows_reviewer_session", "pr_rows_reviewer_override"} {
		if !strings.Contains(plan.String(), "USING INDEX "+index+" (room=? AND <expr>=?)") {
			t.Fatalf("missing indexed session lookup %s in plan:\n%s", index, plan.String())
		}
	}
}
