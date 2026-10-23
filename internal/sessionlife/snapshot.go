package sessionlife

import (
	"time"
)

// Snapshot is lifecycle metadata shared by readers outside the session manager.
// It has no recovery callbacks and can be embedded in a larger metadata record.
type Snapshot struct {
	HistoryOnly bool      `json:"historyOnly,omitempty"`
	ID          string    `json:"id"`
	Closed      time.Time `json:"closed"`
	Closing     *Closing  `json:"closing"`
	Turns       int       `json:"turns"`
}

// ClosedAt returns the stored close time.
func (s Snapshot) ClosedAt() time.Time { return s.Closed }

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
