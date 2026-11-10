package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

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
// Repositories must be detached before changing account identity. A token may
// be replaced for the same login without disturbing attached repositories.
func (s *Store) SetGitHubAccount(ctx context.Context, room contract.RoomID, a GitHubIdentity) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var previous string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT login FROM room_github_accounts WHERE room=r.id),'') FROM rooms r WHERE r.id=?`, room).Scan(&previous); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_repos WHERE room=?`, room).Scan(&count); err != nil {
			return err
		}
		if count != 0 && !strings.EqualFold(previous, a.Login) {
			return errors.New("detach the room's repositories first before changing GitHub account")
		}
		if a.Login == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM room_github_accounts WHERE room=?`, room)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO room_github_accounts(room,login,name,email,credential) VALUES(?,?,?,?,?) ON CONFLICT(room) DO UPDATE SET login=excluded.login,name=excluded.name,email=excluded.email,credential=excluded.credential`, room, a.Login, a.Name, a.Email, a.Credential)
		return err
	})
}
