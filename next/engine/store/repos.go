package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

var ErrRoomNotFound = errors.New("unknown room")

// ErrRepoNotFound means no room lists the repository.
var ErrRepoNotFound = errors.New("unknown repository")

// SetRepoCI replaces a repository's runner hosts. A repository with none is in
// GitHub mode, and one with any is in box mode.
func (s *Store) SetRepoCI(ctx context.Context, repo contract.RepoName, hosts []contract.RepoCIHost) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM repos WHERE repo=?", repo).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRepoNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM repo_ci_hosts WHERE repo=?", repo); err != nil {
			return err
		}
		for _, host := range hosts {
			if _, err := tx.ExecContext(ctx, "INSERT INTO repo_ci_hosts(repo,host,runners,size) VALUES(?,?,?,?)", repo, host.Host, host.Runners, host.Size); err != nil {
				return err
			}
		}
		return nil
	})
}

// CIHosts lists the runner hosts of repositories in box mode.
func (s *Store) CIHosts(ctx context.Context) ([]contract.RepoCIHost, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT repo,host,runners,size FROM repo_ci_hosts ORDER BY repo,host")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []contract.RepoCIHost
	for rows.Next() {
		var host contract.RepoCIHost
		if err := rows.Scan(&host.Repo, &host.Host, &host.Runners, &host.Size); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
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
	rows, err := s.Read().QueryContext(ctx, "SELECT repo,is_default FROM room_repos WHERE room=? ORDER BY repo", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []contract.RoomRepo
	for rows.Next() {
		var repo contract.RoomRepo
		if err := rows.Scan(&repo.Repo, &repo.Default); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
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
