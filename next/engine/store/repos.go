package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

var ErrRoomNotFound = errors.New("unknown room")

// ErrRepoNotFound means no room lists the repository.
var ErrRepoNotFound = errors.New("unknown repository")

// SetRepoCI turns a repository's CI runners on or off. Max bounds them across sizes, and zero leaves them to admission.
func (s *Store) SetRepoCI(ctx context.Context, repo contract.RepoName, on bool, max int) error {
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
		return noRows(result, err, ErrRepoNotFound)
	})
}

// CIRepo is a repository whose CI runs on Dyna's runners, with its room and that room's rule, which its runners follow as
// the room's boxes do.
type CIRepo struct {
	Repo contract.RepoName
	Max  int
	Room contract.RoomID
	Only []string
}

// CIRepos lists the repositories whose CI is on.
func (s *Store) CIRepos(ctx context.Context) ([]CIRepo, error) {
	return queryAll(ctx, s.Read(), func(rows scanner) (CIRepo, error) {
		var repo CIRepo
		var only string
		if err := rows.Scan(&repo.Repo, &repo.Max, &repo.Room, &only); err != nil {
			return repo, err
		}
		if err := json.Unmarshal([]byte(only), &repo.Only); err != nil {
			return repo, err
		}
		return repo, nil
	}, "SELECT p.repo, p.ci_max, p.default_room, r.only_hosts FROM repos p JOIN rooms r ON r.id=p.default_room WHERE p.ci=1 ORDER BY p.repo")
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
