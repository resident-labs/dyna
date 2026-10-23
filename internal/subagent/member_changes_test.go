package subagent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
)

func TestMemberChangesUsesCommittedRecordTurnAndQueueRevisions(t *testing.T) {
	s := revisionStore(t)
	a, b := newSession(t, s, "a"), newSession(t, s, "b")
	revision, _, reset, err := s.MemberChanges("")
	if err != nil || !reset {
		t.Fatalf("initial changes: reset=%v, %v", reset, err)
	}
	assert := func(want ...string) {
		t.Helper()
		next, ids, reset, err := s.MemberChanges(revision)
		slices.Sort(ids)
		slices.Sort(want)
		if err != nil || reset || !slices.Equal(ids, want) {
			t.Fatalf("changes = %v, reset=%v, %v; want %v", ids, reset, err, want)
		}
		revision = next
	}
	assert()
	if _, err := s.UpdateMeta(a, func(m *Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	assert(a)
	if err := s.writeTurn(b, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	assert(b)
	touchDelivery(t, s, a)
	if err := s.writeTurn(a, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	assert(a) // Multiple row writes name one session once.
	assert()
	if err := sessionlife.Delete(s.Root, b); err != nil {
		t.Fatal(err)
	}
	if _, ids, reset, err := s.MemberChanges(revision); err != nil || !reset || len(ids) != 0 {
		t.Fatalf("delete changes = %v, reset=%v, %v", ids, reset, err)
	}
	if _, _, reset, err := s.MemberChanges("invalid token"); err != nil || !reset {
		t.Fatalf("invalid cursor: %v, %v", reset, err)
	}
}

func TestMemberChangesIncludesClosedRecords(t *testing.T) {
	s := revisionStore(t)
	id := newSession(t, s, "closed")
	revision, err := s.MembersRevision()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMeta(id, func(meta *Meta) { meta.Closed = time.Now() }); err != nil {
		t.Fatal(err)
	}
	_, ids, reset, err := s.MemberChanges(revision)
	if err != nil || reset || len(ids) != 1 || ids[0] != id {
		t.Fatalf("closed record delta = %v, reset=%v, %v", ids, reset, err)
	}
	all, err := s.ListSnapshot(true)
	if err != nil || len(all) != 1 || all[0].ID != id {
		t.Fatalf("closed record listing = %+v, %v", all, err)
	}
}

// Every watch tick runs this query on a database whose turn table is large.
// A scan of any table reads all of its rows, so each branch must seek.
func TestMemberChangesQuerySeeksRevisionIndexes(t *testing.T) {
	s := revisionStore(t)
	id := newSession(t, s, "plan")
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	touchDelivery(t, s, id)
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN "+memberChangesQuery, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	text := strings.Join(plan, "\n")
	for _, index := range []string{"session_records_revision", "session_turns_revision", "post_deliveries_updated"} {
		if !strings.Contains(text, index) {
			t.Fatalf("plan does not seek %s:\n%s", index, text)
		}
	}
	for _, detail := range plan {
		if strings.HasPrefix(detail, "SCAN") {
			t.Fatalf("plan scans a table: %s\n%s", detail, text)
		}
	}
}
