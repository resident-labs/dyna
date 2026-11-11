package store

import (
	"strings"
	"testing"
)

func TestPayDiscordReplyPlan(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.Read().Query("EXPLAIN QUERY PLAN "+payDiscordDelivery, "session", "discord:200", "session", "300")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexedEvent := false
	indexedDelivery := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SCAN events") {
			t.Fatalf("payment scans event history: %s", detail)
		}
		indexedEvent = indexedEvent || strings.Contains(detail, "SEARCH events USING INDEX")
		indexedDelivery = indexedDelivery || strings.Contains(detail, "SEARCH deliveries USING INDEX deliveries_by_turn")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexedEvent || !indexedDelivery {
		t.Fatalf("missing indexed delivery/event lookup: delivery=%v event=%v", indexedDelivery, indexedEvent)
	}
}
