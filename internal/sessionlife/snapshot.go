package sessionlife

import (
	"encoding/json"
	"time"
)

// Snapshot is lifecycle metadata shared by readers outside the session manager.
// It has no recovery callbacks and can be embedded in a larger metadata record.
type Snapshot struct {
	ID      string    `json:"id"`
	Closed  time.Time `json:"closed"`
	Closing *Closing  `json:"closing"`
	Turns   int       `json:"turns"`
	// TODO: legacy/shim remove later — old done sessions are dormant, not closed; remove when no retained session.json has done.
	LegacyDone json.RawMessage `json:"done"`
}

// ClosedAt returns the actual close time, including the legacy dormancy rule.
func (s Snapshot) ClosedAt() time.Time {
	var done bool
	if json.Unmarshal(s.LegacyDone, &done) == nil && done {
		return time.Time{}
	}
	return s.Closed
}

func (s Snapshot) IsClosed() bool { return !s.ClosedAt().IsZero() }

// Read reads lifecycle metadata without modifying or recovering a session.
func Read(sessions, id string) (Snapshot, error) {
	var s Snapshot
	err := Load(sessions, id, &s)
	return s, err
}

// TerminalTurn reports the persisted turn statuses that have finished writing.
func TerminalTurn(status string) bool {
	return status == "ok" || status == "error" || status == "stopped"
}
