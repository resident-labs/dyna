package store

import (
	"cmp"
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
		result, err := tx.ExecContext(ctx, "UPDATE repos SET ci=?, ci_max=? WHERE repo=?", on, max, repo)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return cmp.Or(err, ErrRepoNotFound)
		}
		return nil
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
	rows, err := s.Read().QueryContext(ctx, "SELECT p.repo, p.ci_max, p.default_room, r.only_hosts FROM repos p JOIN rooms r ON r.id=p.default_room WHERE p.ci=1 ORDER BY p.repo")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []CIRepo
	for rows.Next() {
		var repo CIRepo
		var only string
		if err := rows.Scan(&repo.Repo, &repo.Max, &repo.Room, &only); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(only), &repo.Only); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
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
	return queryAll(ctx, s.Read(), func(rows scanner) (repo contract.RoomRepo, err error) {
		err = rows.Scan(&repo.Repo, &repo.Default)
		return
	}, "SELECT repo,is_default FROM room_repos WHERE room=? ORDER BY repo", id)
}

// AddRoomRepo updates both defaults in the same transaction.
func (s *Store) AddRoomRepo(ctx context.Context, id contract.RoomID, name contract.RepoName, makeDefault bool) error {
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
		rows, err := tx.QueryContext(ctx, "SELECT repo,is_default FROM room_repos WHERE room=? ORDER BY repo", id)
		if err != nil {
			return err
		}
		var current []contract.RoomRepo
		for rows.Next() {
			var repo contract.RoomRepo
			if err := rows.Scan(&repo.Repo, &repo.Default); err != nil {
				rows.Close()
				return err
			}
			current = append(current, repo)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		var defaultRoom contract.RoomID
		err = tx.QueryRowContext(ctx, "SELECT default_room FROM repos WHERE repo=?", name).Scan(&defaultRoom)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		updated, defaultRoom := room.AddRepo(id, current, defaultRoom, name, makeDefault)
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
		return nil
	})
}
