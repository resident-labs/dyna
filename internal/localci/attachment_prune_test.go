package localci

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
)

// Only a kept run past the attachment TTL whose owner is gone and that holds no
// admitted reservation is deleted. A live owner, a reservation, or a record
// still inside the TTL keeps its record.
func TestPruneAttachmentsDeletesOnlyDeadRecordsPastTTLWithoutReservations(t *testing.T) {
	previousOwner, previousAlive, previousClock := attachmentOwner, ownerAlive, resultClock
	t.Cleanup(func() { attachmentOwner, ownerAlive, resultClock = previousOwner, previousAlive, previousClock })
	const deadPID, livePID = 11, 13
	ownerAlive = func(pid int, _ uint64) bool { return pid == livePID }
	ctx := t.Context()
	s := fixtureStore(t)
	repo, sha := "/repo", strings.Repeat("a", 40)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	save := func(runID string, pid int, at time.Time) {
		t.Helper()
		attachmentOwner = func() (int, uint64) { return pid, 1 }
		resultClock = func() time.Time { return at }
		if err := s.saveAttachment(ctx, repo, sha, "pve", attachment{RunID: runID, Checks: []Suite{SuiteGoStatic}}); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-attachmentTTL - time.Hour)
	save("dead", deadPID, old)
	save("reserved", deadPID, old)
	save("live", livePID, old)
	save("recent", deadPID, now.Add(-time.Hour))
	admission := jobs.Admission{DataDir: s.DataDir, Engine: true}
	if _, err := admission.Hold(ctx, jobs.Demand{Node: "pve", ID: "reserved-" + string(SuiteGoStatic), Room: "room", CPUs: 1, MemoryMiB: 1, Timeout: time.Hour}); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneAttachments(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("PruneAttachments removed %d, %v; want 1", n, err)
	}
	rows, err := s.queryAttachments(ctx, "1=1")
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, row := range rows {
		kept[row.RunID] = true
	}
	for _, id := range []string{"reserved", "live", "recent"} {
		if !kept[id] {
			t.Fatalf("record %s was deleted, want kept", id)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("kept %d records, want 3", len(rows))
	}
}
