package callspec

import "time"

// ClaudeUsageWindow is one usage window of a Claude rate_limit_event. The
// units match saved readings: percent used and a reset time.
type ClaudeUsageWindow struct {
	ID          string    `json:"id"`
	UsedPercent float64   `json:"usedPercent"`
	ResetsAt    time.Time `json:"resetsAt"`
}

// ClaudeUsageObservation reports the usage windows of one admitted attempt's
// matching native identity. Credentials and process output stay in the runner.
type ClaudeUsageObservation struct {
	ExecutionRef
	Identity   string              `json:"identity"`
	ObservedAt time.Time           `json:"observedAt"`
	Windows    []ClaudeUsageWindow `json:"windows"`
}
