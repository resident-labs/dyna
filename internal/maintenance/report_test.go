package maintenance

import (
	"context"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"errors"
	"testing"
	"time"
)

func TestSummaryNamesWhatEachKindFreed(t *testing.T) {
	var r Result
	r.add("closed session stdout", Pruned{Items: 12, Bytes: 3 << 30})
	r.add("scratch", Pruned{Items: 2, Bytes: 5 << 20})
	r.add("worktrees", Pruned{})
	r.Kept = map[string]int{"dirty": 2}
	want := "freed 3.0 GB: closed session stdout 12 (3.0 GB), scratch 2 (5.0 MB); kept 2"
	if got := r.Summary(); got != want {
		t.Fatalf("summary = %q\nwant      %q", got, want)
	}
}

// Every pass is saved, newest first, and the table stays bounded.
func TestReportsAreSavedNewestFirstAndBounded(t *testing.T) {
	dir := t.TempDir()
	start := time.UnixMilli(1_700_000_000_000)
	for n := 0; n < keepReports+3; n++ {
		var r Result
		r.add("scratch", Pruned{Items: 1, Bytes: int64(n)})
		var runErr error
		if n == keepReports+2 {
			runErr = errors.New("one sweep failed")
		}
		if err := SaveReport(dir, start.Add(time.Duration(n)*time.Hour), time.Second, r, runErr); err != nil {
			t.Fatal(err)
		}
	}
	all, err := RecentReports(dir, keepReports+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != keepReports {
		t.Fatalf("kept %d reports, want %d", len(all), keepReports)
	}
	newest := all[0]
	if newest.BytesFreed != keepReports+2 || newest.Error != "one sweep failed" || newest.Duration != time.Second || !newest.At.Equal(start.Add((keepReports+2)*time.Hour)) {
		t.Fatalf("newest report = %+v", newest)
	}
}

// A saved pass keeps each step's order and the error text of a failed step.
func TestReportKeepsEachStepWithItsError(t *testing.T) {
	dir := t.TempDir()
	var r Result
	done := r.timer("worktrees")
	done(nil)
	done = r.timer("scratch")
	done(errors.New("context deadline exceeded"))
	if err := SaveReport(dir, time.Unix(100, 0), time.Second, r, nil); err != nil {
		t.Fatal(err)
	}
	reports, err := RecentReports(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := reports[0].Steps
	if len(got) != 2 || got[0].Name != "worktrees" || got[0].Error != "" || got[1].Name != "scratch" || got[1].Error != "context deadline exceeded" {
		t.Fatalf("steps = %+v", got)
	}
}

func TestReportUsesWriterWithoutUnusedEvents(t *testing.T) {
	dir := t.TempDir()
	db, err := reportDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("unused event: %+v", event) })
	if err := SaveReport(dir, time.Unix(100, 0), time.Millisecond, Result{}, nil); err != nil {
		t.Fatal(err)
	}
	reports, err := RecentReports(dir, 1)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %v, %v", reports, err)
	}
	if got := writer.Health().Writes; got != 1 {
		t.Fatalf("writer recorded %d writes, want 1", got)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_report BEFORE INSERT ON maintenance_reports BEGIN SELECT RAISE(ABORT, 'rejected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := SaveReport(dir, time.Unix(101, 0), time.Millisecond, Result{}, nil); err == nil {
		t.Fatal("rejected report succeeded")
	}
	reports, err = RecentReports(dir, 2)
	if err != nil || len(reports) != 1 {
		t.Fatalf("failed write changed reports: %v, %v", reports, err)
	}
}

func TestDryPassUsesWriterWithoutUnusedEvents(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := reportDB(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("unused event: %+v", event) })
	var result Result
	for _, wantDry := range []bool{true, false} {
		if err := sweepWithDryPass(context.Background(), &result, "test", time.Hour, func(dry bool) (Pruned, error) {
			if dry != wantDry {
				t.Errorf("dry = %v, want %v", dry, wantDry)
			}
			return Pruned{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := writer.Health().Writes; got != 1 {
		t.Fatalf("writer recorded %d writes, want 1", got)
	}
	var done string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, dryPassKey("test", time.Hour)).Scan(&done); err != nil || done != "1" {
		t.Fatalf("marker = %q, %v", done, err)
	}
}
