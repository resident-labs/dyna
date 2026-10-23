package subagent

import (
	"fmt"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// MembersRevision changes whenever a session row, a turn row or a delivery row
// is written, or a session row is deleted (requirement 16). It reads the
// newest session record, the highest turn revision, the newest delivery write
// and the delete counter, so a watcher that saw the same value has nothing new
// to read. Turn rows are never deleted, so their highest revision cannot be
// reused; every delivery write moves updated_at forward (see room's
// deliverystate.go), so its newest value cannot either.
func (s *Store) MembersRevision() (string, error) {
	if _, err := s.turnsDB(); err != nil {
		return "", err
	}
	records, err := sessionlife.Revision(s.root())
	if err != nil {
		return "", err
	}
	if _, err := queuesDB(s.root()); err != nil {
		return "", err
	}
	if err := room.MigrateDeliveries(filepath.Dir(s.root())); err != nil {
		return "", err
	}
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return "", err
	}
	var turns, queues int64
	var deletes string
	err = db.QueryRow(`SELECT
  (SELECT COALESCE(MAX(revision), 0) FROM session_turns),
  (SELECT COALESCE(MAX(updated_at), 0) FROM post_deliveries),
  COALESCE((SELECT value FROM meta WHERE key = ?), '0')`, sessionlife.DeletesKey).Scan(&turns, &queues, &deletes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|t%d|q%d|d%s", records, turns, queues, deletes), nil
}
