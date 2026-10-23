package doctor

import (
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
	"testing"
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
