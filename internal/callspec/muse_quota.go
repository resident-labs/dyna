package callspec

import "time"

// MuseQuotaObservation reports one admitted attempt's matching native identity
// and exhaustion times. Credentials and process output stay in the runner.
type MuseQuotaObservation struct {
	ExecutionRef
	Identity   string    `json:"identity"`
	ObservedAt time.Time `json:"observedAt"`
	ResetsAt   time.Time `json:"resetsAt"`
}
