package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

type GitHubIdentity struct {
	Login, Name, Email string
	Credential         contract.RoomID
}

func (s *Store) GitHubAccount(ctx context.Context, room contract.RoomID) (GitHubIdentity, error) {
	var a GitHubIdentity
	err := s.Read().QueryRowContext(ctx, `SELECT COALESCE(a.login,''),COALESCE(a.name,''),COALESCE(a.email,''),COALESCE(a.credential,'') FROM rooms r LEFT JOIN room_github_accounts a ON a.room=r.id WHERE r.id=?`, room).Scan(&a.Login, &a.Name, &a.Email, &a.Credential)
	return a, err
}
func (s *Store) RepoRoom(ctx context.Context, repo contract.RepoName) (contract.RoomID, error) {
	var room contract.RoomID
	err := s.Read().QueryRowContext(ctx, `SELECT default_room FROM repos WHERE repo=?`, repo).Scan(&room)
	return room, err
}

// SetGitHubAccount changes metadata only; credentials are never stored in SQLite.
// Shared repositories and CI must be removed before changing account identity.
func (s *Store) SetGitHubAccount(ctx context.Context, room contract.RoomID, a GitHubIdentity) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_repos rr JOIN repos p ON p.repo=rr.repo WHERE rr.room=? AND (p.ci=1 OR EXISTS(SELECT 1 FROM room_repos other WHERE other.repo=rr.repo AND other.room<>rr.room))`, room).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("remove shared repositories and disable CI before changing GitHub account")
		}
		if a.Login == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM room_github_accounts WHERE room=?`, room)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO room_github_accounts(room,login,name,email,credential) VALUES(?,?,?,?,?) ON CONFLICT(room) DO UPDATE SET login=excluded.login,name=excluded.name,email=excluded.email,credential=excluded.credential`, room, a.Login, a.Name, a.Email, a.Credential)
		return err
	})
}
