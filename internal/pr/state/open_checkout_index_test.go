package state

import (
	"strings"
	"testing"
)

func TestOpenTrackedCheckoutUsesOpenIndex(t *testing.T) {
	db, err := open(newRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN " + openTrackedCheckoutQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || !strings.Contains(plans[0], "pr_rows_open") {
		t.Fatalf("open checkout lookup scans closed PRs: %v", plans)
	}
}
