package localci

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
)

// A stale run left by an engine that restarted keeps its record and its
// reservations. Abandoning it tears the record down once, releases the run's two
// reservations, and leaves the reservation of another run untouched.
func TestAbandonRunReleasesAStaleRunsReservations(t *testing.T) {
	previousOwner, previousAlive := attachmentOwner, ownerAlive
	t.Cleanup(func() { attachmentOwner, ownerAlive = previousOwner, previousAlive })
	ownerAlive = func(int, uint64) bool { return false }
	dir := t.TempDir()
	store := Store{DataDir: dir}
	repo, sha := "/repo", strings.Repeat("a", 40)
	run := RunID("e8cb25882c1b-1793312721843629186")
	other := RunID("0123456789ab-1793312721843629187")
	// A legacy record: it has no source.
	attachmentOwner = func() (int, uint64) { return 11, 1 }
	if err := store.saveAttachment(t.Context(), repo, sha, "pve", attachment{RunID: string(run), Checks: []Suite{SuiteGoStatic}}); err != nil {
		t.Fatal(err)
	}
	admission := jobs.Admission{DataDir: dir, Engine: true}
	for _, id := range []string{string(run) + "-" + string(SuiteGoStatic), string(run) + "-go-unit", string(other) + "-" + string(SuiteGoStatic)} {
		if _, err := admission.Hold(t.Context(), jobs.Demand{Node: "pve", ID: id, Room: "room", CPUs: 1, MemoryMiB: 1, Timeout: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	var removed atomic.Int32
	machine := newFakeMachine(t, func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
		script := args[len(args)-1]
		if strings.Contains(script, "rm -rf --one-file-system") && !strings.Contains(script, "trap ") {
			removed.Add(1)
		}
		return nil
	})
	runner := Runner{DataDir: dir, Node: &Node{Name: "pve", SSH: "pip@pve", Enabled: true}, Engine: true}
	machine.attach(&runner)

	// The engine runs this run now, so its admitted rows store no owner process
	// and the owner check cannot see it. The teardown refuses and releases nothing.
	if _, err := runner.AbandonRun(t.Context(), store, run, []string{"e8cb258"}); !errors.Is(err, ErrRunLive) {
		t.Fatalf("abandoning a run in flight returned %v, want ErrRunLive", err)
	}
	if removed.Load() != 0 {
		t.Fatalf("refused teardown removed the run folder %d times", removed.Load())
	}
	if left, err := admission.RunReservations(t.Context(), string(run)); err != nil || len(left) != 2 {
		t.Fatalf("refused teardown released reservations: %v, %v", left, err)
	}

	// Past the attachment TTL the record is not claimed, so the command refuses and releases nothing.
	previousClock := resultClock
	t.Cleanup(func() { resultClock = previousClock })
	resultClock = func() time.Time { return time.Now().Add(4 * time.Hour) }
	if _, err := runner.AbandonRun(t.Context(), store, run, nil); !errors.Is(err, ErrRunUnclaimed) {
		t.Fatalf("abandoning an expired record returned %v, want ErrRunUnclaimed", err)
	}
	if left, err := admission.RunReservations(t.Context(), string(run)); err != nil || len(left) != 2 || removed.Load() != 0 {
		t.Fatalf("expired record refusal released or removed something: %v, %v, removed %d", left, err, removed.Load())
	}
	resultClock = previousClock

	got, err := runner.AbandonRun(t.Context(), store, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := removed.Load(); n != 1 {
		t.Fatalf("teardown removed the run folder %d times, want once", n)
	}
	if got.Reservations != 2 {
		t.Fatalf("released %d reservations, want 2", got.Reservations)
	}
	if rows, err := store.attachmentRows(t.Context(), repo, sha); err != nil || len(rows) != 0 {
		t.Fatalf("the run record survived the teardown: %+v, %v", rows, err)
	}
	if left, err := admission.RunReservations(t.Context(), string(run)); err != nil || len(left) != 0 {
		t.Fatalf("reservations of the abandoned run remain: %v, %v", left, err)
	}
	if left, err := admission.RunReservations(t.Context(), string(other)); err != nil || len(left) != 1 {
		t.Fatalf("another run's reservation was touched: %v, %v", left, err)
	}
}
