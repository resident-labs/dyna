package subagent

import (
	"fmt"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
)

// MembersRevision changes whenever a session row, a turn row or a queue row
// is written, or a session row is deleted (requirement 16). It reads the
// newest session record, the highest turn and queue revisions, and the
// delete counter, so a watcher that saw the same value has nothing new to
// read. Turn and queue rows are never deleted, so their highest revision
// cannot be reused.
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
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return "", err
	}
	var turns, queues int64
	var deletes string
	err = db.QueryRow(`SELECT
  (SELECT COALESCE(MAX(revision), 0) FROM session_turns),
  (SELECT COALESCE(MAX(revision), 0) FROM session_queues),
  COALESCE((SELECT value FROM meta WHERE key = ?), '0')`, sessionlife.DeletesKey).Scan(&turns, &queues, &deletes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|t%d|q%d|d%s", records, turns, queues, deletes), nil
}
