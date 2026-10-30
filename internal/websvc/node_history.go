package websvc

import (
	"context"
	"os"
	"time"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/store"
)

// NodeHistoryPoint is one recorded CPU and memory reading. A missing CPU sample
// stays null; it is never presented as zero.
type NodeHistoryPoint struct {
	At            time.Time `json:"at"`
	CPUPercent    *float64  `json:"cpuPercent" tstype:"number | null"`
	MemoryPercent *float64  `json:"memoryPercent" tstype:"number | null"`
}

// NodeDiagnosis is the last machine-wide doctor report, without internal IDs.
type NodeDiagnosis struct {
	At       time.Time              `json:"at"`
	Healthy  bool                   `json:"healthy"`
	Summary  string                 `json:"summary"`
	Findings []NodeDiagnosisFinding `json:"findings"`
}

// NodeDiagnosisFinding is one cause and its evidence.
type NodeDiagnosisFinding struct {
	Cause    string `json:"cause"`
	Evidence string `json:"evidence"`
}

// historyKey matches local sampler history and identifies remote nodes by ID.
func historyKey(m machines.Machine) string {
	if m.Local {
		host, _ := os.Hostname()
		return host
	}
	return m.ID
}

func (a MachineAPI) historyDir() string {
	if a.Store.DataDir != "" {
		return a.Store.DataDir
	}
	return store.DataDir()
}

// NodeHistory reads the last 24 hours of the node's machine samples.
func (a MachineAPI) NodeHistory(ctx context.Context, id string) ([]NodeHistoryPoint, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return nil, err
	}
	samples, err := (doctor.Samples{DataDir: a.historyDir()}).Window(historyKey(m), "machine", time.Now().Add(-doctor.Keep))
	if err != nil {
		return nil, err
	}
	points := make([]NodeHistoryPoint, 0, len(samples))
	for _, sample := range samples {
		point := NodeHistoryPoint{At: sample.At, CPUPercent: sample.CPUPercent}
		if sample.MemoryTotal > 0 {
			percent := float64(sample.Group.Memory) * 100 / float64(sample.MemoryTotal)
			point.MemoryPercent = &percent
		}
		points = append(points, point)
	}
	return points, nil
}

// NodeLastDiagnosis reads the latest machine-wide report, or null when absent.
func (a MachineAPI) NodeLastDiagnosis(ctx context.Context, id string) (*NodeDiagnosis, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return nil, err
	}
	report, found, err := (doctor.Reports{DataDir: a.historyDir()}).LatestMachine(historyKey(m))
	if err != nil || !found {
		return nil, err
	}
	out := &NodeDiagnosis{At: report.At, Healthy: report.Healthy, Summary: report.Summary, Findings: make([]NodeDiagnosisFinding, 0, len(report.Findings))}
	for _, finding := range report.Findings {
		out.Findings = append(out.Findings, NodeDiagnosisFinding{Cause: finding.Cause, Evidence: finding.Evidence})
	}
	return out, nil
}
