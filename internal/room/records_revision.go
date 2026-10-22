package room

import (
	"path/filepath"

	"dyna-agent/internal/database"
)

// RecordsRevision identifies committed room, seat and pause changes, including
// deletions. Reading it never imports files or runs a store migration.
func (s *Store) RecordsRevision() (int64, error) {
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return 0, err
	}
	var revision int64
	err = db.QueryRow("SELECT COALESCE(CAST((SELECT value FROM meta WHERE key = ?) AS INTEGER), 0)", RevisionKey).Scan(&revision)
	return revision, err
}
