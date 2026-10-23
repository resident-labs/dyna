package doctor

import (
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
	"testing"
	"time"
)

func TestReportWakesRoot(t *testing.T) {
	in := reportPost("work", doctor.Report{ID: 1, Summary: "machine is overloaded"})
	again := reportPost("work", doctor.Report{ID: 1, Summary: "machine is overloaded"})
	if in.ID == "" || in.ID != again.ID {
		t.Fatal("report retry changed its stable post ID")
	}
	decision, _ := room.RootContext(room.Message{From: in.From, To: in.To, Kind: in.Kind, Wake: in.Wake}, "work")
	if decision != room.RootWake {
		t.Fatalf("decision = %v, want RootWake", decision)
	}
}

func TestReportPostIDSurvivesPersistence(t *testing.T) {
	report := doctor.Report{At: time.Unix(123456, 789), Machine: "laptop", Summary: "machine is overloaded"}
	reports := doctor.Reports{DataDir: t.TempDir()}
	var err error
	report.ID, err = reports.Save(report)
	if err != nil {
		t.Fatal(err)
	}
	saved, found, err := reports.Get(report.ID)
	if err != nil || !found {
		t.Fatalf("load: found=%v, err=%v", found, err)
	}
	if before, after := reportPost("work", report).ID, reportPost("work", saved).ID; before != after {
		t.Fatalf("post ID changed after persistence: %s to %s", before, after)
	}
}
