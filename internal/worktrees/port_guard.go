package worktrees

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// portGuard ties an ownership scan to the writer transaction. Revisions also
// detect owners added after inference, not just changes to owners it found.
type portGuard struct {
	revision portOwnerRevision
	owners   inferredOwners
}

type portOwnerRevision struct {
	sessionID                 string
	sessionRevision, sessions int64
	deletes, goals            string
	runs                      int64
}

func readPortOwnerRevision(ctx context.Context, q state.Reader) (portOwnerRevision, error) {
	var r portOwnerRevision
	err := q.QueryRowContext(ctx, `SELECT
COALESCE((SELECT id FROM session_records ORDER BY revision DESC LIMIT 1), ''),
COALESCE((SELECT MAX(revision) FROM session_records), 0),
(SELECT COUNT(*) FROM session_records),
COALESCE((SELECT value FROM meta WHERE key='sessions.deletes'), '0'),
COALESCE((SELECT value FROM meta WHERE key='goals.revision'), '0'),
(SELECT n FROM runs_rev)`).Scan(&r.sessionID, &r.sessionRevision, &r.sessions, &r.deletes, &r.goals, &r.runs)
	return r, err
}

func beginPortGuard(ctx context.Context, db *sql.DB) (*portGuard, error) {
	// Initialize the canonical tables before taking the revision snapshot.
	if err := sessionlife.Owners(runstore.SessionsDir(), func(sessionlife.OwnerRow) error { return nil }); err != nil {
		return nil, err
	}
	if _, err := runstore.StoredMetadata(); err != nil {
		return nil, err
	}
	if _, err := goals.DefaultStore().ReadOpen(); err != nil {
		return nil, err
	}
	rev, err := readPortOwnerRevision(ctx, db)
	return &portGuard{revision: rev}, err
}

func (g *portGuard) infer(ctx context.Context, rec Record) error {
	owners, err := inferOwners(ctx, []string{rec.Path}, nil)
	if err != nil {
		return err
	}
	g.owners = owners[rec.Path]
	return nil
}

// releaseClosedPorts runs after session cleanup and scheduled cleanup. It
// preserves trees and clears only blocks with a fresh, transactional proof.
func releaseClosedPorts(ctx context.Context, repo string) error {
	db, err := registryDB()
	if err != nil {
		return err
	}
	records, err := loadRecords(ctx, db, "")
	if err != nil {
		return err
	}
	var candidates []Record
	var paths []string
	for _, rec := range records {
		if rec.Repo == repo && rec.Port != 0 {
			candidates = append(candidates, rec)
			paths = append(paths, rec.Path)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	base, err := beginPortGuard(ctx, db)
	if err != nil {
		return err
	}
	eligible := reclaimablePaths(ctx, repo)
	owners, err := inferOwners(ctx, paths, nil)
	if err != nil {
		return err
	}
	var releases []portRelease
	for _, rec := range candidates {
		if !eligible[filepath.Clean(rec.Path)] || !blockFree(rec.Port, portFree) {
			continue
		}
		guard := *base
		guard.owners = owners[rec.Path]
		releases = append(releases, portRelease{record: rec, guard: &guard})
	}
	if len(releases) == 0 {
		return nil
	}
	_, err = releaseRecords(ctx, db, releases)
	return err
}

func (g *portGuard) valid(ctx context.Context, conn *sql.Conn, rec Record) (bool, error) {
	rev, err := readPortOwnerRevision(ctx, conn)
	if err != nil || rev != g.revision {
		return false, err
	}
	sessions := map[string]bool{}
	runs := map[string]bool{}
	if rec.Session != "" {
		sessions[rec.Session] = true
	}
	if rec.RunID != "" {
		runs[rec.RunID] = true
	}
	for _, owner := range g.owners.Sessions {
		sessions[owner.ID] = true
	}
	for _, owner := range g.owners.Runs {
		runs[owner.ID] = true
	}
	if len(sessions)+len(runs) == 0 {
		return false, nil
	}
	for id := range sessions {
		var m struct {
			sessionlife.Snapshot
			Room string `json:"room"`
		}
		if err := sessionlife.LoadStored(ctx, conn, id, &m); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if !m.IsClosed() {
			return false, nil
		}
		if m.Room != "" {
			var held bool
			err := conn.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM goals WHERE room=? AND state='active'
AND EXISTS (SELECT 1 FROM json_each(body,'$.holders') WHERE value=?)
UNION ALL SELECT 1 FROM goal_items WHERE room=? AND assignee=? AND state='open')`,
				m.Room, id, m.Room, id).Scan(&held)
			if err != nil || held {
				return false, err
			}
		}
	}
	for id := range runs {
		meta, err := runstore.LoadStored(ctx, conn, id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if meta.ID != id || !runstore.IsFinishedStatus(meta.Status) || meta.EndedAt.IsZero() {
			return false, nil
		}
	}
	return true, nil
}
