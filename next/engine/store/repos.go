package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

var ErrRoomNotFound = errors.New("unknown room")

// ErrRepoNotFound means no room lists the repository.
var ErrRepoNotFound = errors.New("unknown repository")

// SetRepoCI turns a repository's CI runners on or off. Max bounds them across sizes, and zero leaves them to admission.
func (s *Store) SetRepoCI(ctx context.Context, repo contract.RepoName, on bool, max int, hosts ...[]string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if on {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos p JOIN room_github_accounts a ON a.room=p.default_room WHERE p.repo=?`, repo).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return errors.New("CI is unavailable for repositories with a room GitHub account")
			}
		}
		result, err := tx.ExecContext(ctx, "UPDATE repos SET ci=?, ci_max=? WHERE repo=?", on, max, repo)
		if err := noRows(result, err, ErrRepoNotFound); err != nil {
			return err
		}
		if len(hosts) > 0 && hosts[0] != nil {
			encoded, err := json.Marshal(hosts[0])
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE room_repos SET ci_hosts=? WHERE repo=? AND room=(SELECT default_room FROM repos WHERE repo=?)", string(encoded), repo, repo)
			return err
		}
		return nil
	})
}

// CIRepo is a repository whose CI runs on Dyna, with its effective host rule.
type CIRepo struct {
	Repo  contract.RepoName
	Max   int
	Room  contract.RoomID
	Only  []string
	Hosts []string // The repository override, empty when it follows the room.
}

// CIRepos lists the repositories whose CI is on.
func (s *Store) CIRepos(ctx context.Context) ([]CIRepo, error) {
	return queryAll(ctx, s.Read(), func(rows scanner) (CIRepo, error) {
		var repo CIRepo
		var only, hosts string
		if err := rows.Scan(&repo.Repo, &repo.Max, &repo.Room, &only, &hosts); err != nil {
			return repo, err
		}
		if err := json.Unmarshal([]byte(only), &repo.Only); err != nil {
			return repo, err
		}
		if err := json.Unmarshal([]byte(hosts), &repo.Hosts); err != nil {
			return repo, err
		}
		if len(repo.Hosts) > 0 {
			repo.Only = repo.Hosts
		} else {
			repo.Hosts = nil
		}
		return repo, nil
	}, "SELECT p.repo, p.ci_max, p.default_room, r.only_hosts, COALESCE(rr.ci_hosts, '[]') FROM repos p JOIN rooms r ON r.id=p.default_room LEFT JOIN room_repos rr ON rr.room=p.default_room AND rr.repo=p.repo WHERE p.ci=1 ORDER BY p.repo")
}

// RoomRepos returns the repositories listed by a room.
func (s *Store) RoomRepos(ctx context.Context, id contract.RoomID) ([]contract.RoomRepo, error) {
	var exists int
	if err := s.Read().QueryRowContext(ctx, "SELECT 1 FROM rooms WHERE id=?", id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRoomNotFound
		}
		return nil, err
	}
	return roomRepos(ctx, s.Read(), id)
}

func roomRepos(ctx context.Context, q sqlQuery, id contract.RoomID) ([]contract.RoomRepo, error) {
	return queryAll(ctx, q, func(rows scanner) (repo contract.RoomRepo, err error) {
		err = rows.Scan(&repo.Repo, &repo.Default, &repo.PREvents)
		return
	}, "SELECT repo,is_default,pr_events FROM room_repos WHERE room=? ORDER BY repo", id)
}

// AddRoomRepo updates both defaults in the same transaction.
func (s *Store) AddRoomRepo(ctx context.Context, id contract.RoomID, name contract.RepoName, makeDefault bool, prEvents ...*bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM rooms WHERE id=?", id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return err
		}
		if err := refuseNewWork(ctx, tx, id); err != nil {
			return err
		}
		current, err := roomRepos(ctx, tx, id)
		if err != nil {
			return err
		}
		var defaultRoom contract.RoomID
		err = tx.QueryRowContext(ctx, "SELECT default_room FROM repos WHERE repo=?", name).Scan(&defaultRoom)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		updated, defaultRoom := room.AddRepo(id, current, defaultRoom, name, makeDefault)
		var incompatible int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms r WHERE r.id=? AND COALESCE((SELECT login FROM room_github_accounts WHERE room=r.id),'') COLLATE NOCASE <> COALESCE((SELECT login FROM room_github_accounts WHERE room=?),'') COLLATE NOCASE`, id, defaultRoom).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible > 0 {
			return errors.New("repository and room use different GitHub accounts")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO repos(repo,default_room) VALUES(?,?) ON CONFLICT(repo) DO NOTHING", name, defaultRoom); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO repo_registrations(repo,generation) VALUES(?,lower(hex(randomblob(16)))) ON CONFLICT(repo) DO NOTHING", name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE room_repos SET is_default=0 WHERE room=?", id); err != nil {
			return err
		}
		for _, repo := range updated {
			if _, err := tx.ExecContext(ctx, "INSERT INTO room_repos(room,repo,is_default) VALUES(?,?,?) ON CONFLICT(room,repo) DO UPDATE SET is_default=excluded.is_default", id, repo.Repo, repo.Default); err != nil {
				return err
			}
		}
		if len(prEvents) > 0 && prEvents[0] != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE room_repos SET pr_events=?, pr_census_at=CASE WHEN ? THEN pr_census_at ELSE 0 END WHERE room=? AND repo=?", *prEvents[0], *prEvents[0], id, name); err != nil {
				return err
			}
			if !*prEvents[0] {
				if _, err := tx.ExecContext(ctx, "DELETE FROM room_pr_observed WHERE room=? AND repo=?", id, name); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ErrRoomRepoInUse refuses to remove a room's default or a repository it still uses.
var ErrRoomRepoInUse = errors.New("cannot remove repository")

// RemoveRoomRepo removes just this room's link. The remaining rooms retain shared polling and CI.
func (s *Store) RemoveRoomRepo(ctx context.Context, room contract.RoomID, repo contract.RepoName) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := roomOpen(ctx, tx, room); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRoomNotFound
			}
			return err
		}
		var isDefault bool
		if err := tx.QueryRowContext(ctx, `SELECT is_default FROM room_repos WHERE room=? AND repo=?`, room, repo).Scan(&isDefault); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s is not listed in this room", ErrRepoNotFound, repo)
			}
			return err
		}
		if isDefault {
			return fmt.Errorf("%w %s: it is the room default; make another repository the default first", ErrRoomRepoInUse, repo)
		}
		blockers, err := listIDs[string](ctx, tx, `SELECT 'session ' || name || ' (' || id || ')' FROM sessions WHERE room=?1 AND repo=?2 COLLATE NOCASE AND closed_at IS NULL
   UNION ALL SELECT 'PR #' || p.number || ' (' || p.repo || ')' FROM pr_owners p JOIN sessions s ON s.id=p.session WHERE s.room=?1 AND (p.repo=?2 COLLATE NOCASE OR p.head_repo=?2 COLLATE NOCASE) AND p.state='open'
   UNION ALL SELECT 'issue #' || i.number FROM issue_owners i JOIN sessions s ON s.id=i.session WHERE s.room=?1 AND i.repo=?2 COLLATE NOCASE AND i.state='open'
   ORDER BY 1`, room, repo)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return fmt.Errorf("%w %s: still used by %s", ErrRoomRepoInUse, repo, strings.Join(blockers, ", "))
		}
		var next sql.NullString
		var ci bool
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT MIN(room) FROM room_repos WHERE repo=?2 AND room<>?1), ci FROM repos WHERE repo=?2`, room, repo).Scan(&next, &ci); err != nil {
			return err
		}
		if !next.Valid && ci {
			return fmt.Errorf("%w %s: Dyna CI is on; wait for running jobs to finish, turn it off with dyna room repo ci %s --off, then remove it", ErrRoomRepoInUse, repo, repo)
		}
		// Remaining tracking rows of this room are closed. Leaving them would keep the poller watching for reopens.
		for _, query := range []string{
			`DELETE FROM pr_owners WHERE repo=?2 COLLATE NOCASE AND session IN (SELECT id FROM sessions WHERE room=?1)`,
			`DELETE FROM issue_owners WHERE repo=?2 COLLATE NOCASE AND session IN (SELECT id FROM sessions WHERE room=?1)`,
			`DELETE FROM room_repos WHERE room=?1 AND repo=?2`, // Cascades to room_pr_observed.
		} {
			if _, err := tx.ExecContext(ctx, query, room, repo); err != nil {
				return err
			}
		}
		// Unlinking drops this room's snapshots. A re-added link cannot use a continuation
		// that assumed it had received earlier pages, or a pass read for the old link.
		if _, err := tx.ExecContext(ctx, `DELETE FROM github_feeds WHERE repo=? AND feed=?`, repo, RoomPRCensusFeed); err != nil {
			return err
		}
		if next.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE repo_registrations SET generation=lower(hex(randomblob(16))) WHERE repo=?`, repo); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE repos SET default_room=? WHERE repo=? AND default_room=?`, next.String, repo, room)
			return err
		}
		// A different room can track this repository without listing it; keep its live cursors.
		for _, query := range []string{
			`DELETE FROM github_feeds WHERE repo=?1 AND NOT EXISTS (SELECT 1 FROM pr_owners WHERE repo=?1) AND NOT EXISTS (SELECT 1 FROM issue_owners WHERE repo=?1)`,
			`DELETE FROM repo_heads WHERE repo=?`,
			`DELETE FROM repos WHERE repo=?`,
		} {
			if _, err := tx.ExecContext(ctx, query, repo); err != nil {
				return err
			}
		}
		return nil
	})
}

// requireTrackerRepo checks a session's authorization in the same transaction as the tracking write.
func requireTrackerRepo(ctx context.Context, tx *sql.Tx, session contract.SessionID, repo, head contract.RepoName) error {
	var listed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM room_repos r JOIN sessions s ON s.room=r.room WHERE s.id=? AND r.repo IN (?,?))`, session, repo, head).Scan(&listed); err != nil {
		return err
	}
	if !listed {
		return ErrRepoNotFound
	}
	return nil
}
