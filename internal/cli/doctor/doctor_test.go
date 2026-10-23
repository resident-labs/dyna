package doctor

import (
	"testing"
	"time"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
)

// A doctor report is one typed alert from the engine. It wakes the root seat,
// and a retried report of the same ID keeps its single post.
func TestDoctorReportPostsOneAlertThatWakesRoot(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	report := doctor.Report{ID: 1, Machine: "laptop", Trigger: "hook main-updated", Summary: "machine is overloaded"}
	first, err := postReport(store, rec, report)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "alert" || first.Notice.Kind != "doctor" || first.From != room.EngineSender {
		t.Fatalf("stored alert = %+v", first)
	}
	if !first.Wake || len(first.To) != 1 || first.To[0] != rec.Name+":root" {
		t.Fatalf("alert routing = %+v", first)
	}
	if decision, _ := room.RootContext(first, rec.Name); decision != room.RootWake {
		t.Fatalf("root decision = %v, want RootWake", decision)
	}
	payload, ok := first.Alert()
	if !ok || payload.Source != room.AlertDoctor || payload.ReportID != "1" || payload.Summary != "machine is overloaded" {
		t.Fatalf("payload = %+v, %v", payload, ok)
	}
	again, err := postReport(store, rec, report)
	if err != nil || again.ID != first.ID {
		t.Fatalf("second report of one ID = %+v, %v", again, err)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("stored posts = %d, %v", len(msgs), err)
	}
}

// The report's post ID comes from the report ID alone, so a report saved and
// read back posts the same ID.
func TestReportPostIDSurvivesPersistence(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	report := doctor.Report{At: time.Unix(123456, 789), Machine: "laptop", Summary: "machine is overloaded"}
	reports := doctor.Reports{DataDir: t.TempDir()}
	report.ID, err = reports.Save(report)
	if err != nil {
		t.Fatal(err)
	}
	saved, found, err := reports.Get(report.ID)
	if err != nil || !found {
		t.Fatalf("load: found=%v, err=%v", found, err)
	}
	before, err := postReport(store, rec, report)
	if err != nil {
		t.Fatal(err)
	}
	after, err := postReport(store, rec, saved)
	if err != nil || after.ID != before.ID {
		t.Fatalf("same report after persistence = %+v, %v (before %s)", after, err, before.ID)
	}
}

// A later report with its own ID is a new alert.
func TestEachReportIPostsItsOwnAlert(t *testing.T) {
	store := &room.Store{Root: t.TempDir()}
	rec, err := store.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := postReport(store, rec, doctor.Report{ID: 1, Summary: "machine is overloaded"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postReport(store, rec, doctor.Report{ID: 2, Summary: "machine is overloaded"})
	if err != nil || second.ID == first.ID {
		t.Fatalf("second report = %+v, %v (first %s)", second, err, first.ID)
	}
}
