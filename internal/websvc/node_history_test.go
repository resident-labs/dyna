package websvc

import (
	"testing"
	"time"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/nodehistory"
)

func TestNodeHistoryAndLatestDiagnosis(t *testing.T) {
	api := machineAPI(t)
	if err := doctor.Migrate(api.Store.DataDir); err != nil {
		t.Fatal(err)
	}
	node, err := api.Store.Save(t.Context(), machines.Machine{Name: "builder", SSH: "builder", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	cpu := 81.0
	result := machines.ProbeResult{At: now, Load: machines.Load{CPUs: 8, CPUPercent: &cpu, MemTotal: 100, MemAvailable: 60}}
	if err := nodehistory.Record(doctor.Samples{DataDir: api.Store.DataDir}, node, result); err != nil {
		t.Fatal(err)
	}
	points, err := api.NodeHistory(t.Context(), node.ID)
	if err != nil || len(points) != 1 || points[0].CPUPercent == nil || *points[0].CPUPercent != 81 || points[0].MemoryPercent == nil || *points[0].MemoryPercent != 40 {
		t.Fatalf("history = %+v, %v", points, err)
	}
	reports := doctor.Reports{DataDir: api.Store.DataDir}
	if _, err := reports.Save(doctor.Report{At: now, Machine: node.ID, Target: "machine", Healthy: true, Summary: "Healthy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reports.Save(doctor.Report{At: now.Add(time.Second), Machine: "another", Target: "machine", Summary: "Other"}); err != nil {
		t.Fatal(err)
	}
	diagnosis, err := api.NodeLastDiagnosis(t.Context(), node.ID)
	if err != nil || diagnosis == nil || !diagnosis.Healthy || diagnosis.Summary != "Healthy" {
		t.Fatalf("diagnosis = %+v, %v", diagnosis, err)
	}
}
