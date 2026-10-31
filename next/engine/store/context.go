package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/resident-labs/dyna/contract"
)

type ContextChild struct {
	Name    string
	Profile contract.ProfileName
	State   contract.BoxState
}
type ContextRequest struct {
	ID   contract.OpID
	Kind contract.OpKind
}
type ContextPR struct {
	Repo     contract.RepoName
	Number   int
	Approved bool
}

func (s *Store) ContextChildren(ctx context.Context, id contract.SessionID) ([]ContextChild, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT s.name,s.profile,b.state FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.parent=? AND s.closed_at IS NULL ORDER BY COALESCE((SELECT MAX(t.started_at) FROM turns t WHERE t.session=s.id),s.created_at),s.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextChild
	for rows.Next() {
		var c ContextChild
		if err := rows.Scan(&c.Name, &c.Profile, &c.State); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ContextRequests(ctx context.Context, id contract.SessionID) ([]ContextRequest, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT id,kind FROM operations WHERE state='pending' AND by_kind='session' AND by_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextRequest
	for rows.Next() {
		var r ContextRequest
		if err := rows.Scan(&r.ID, &r.Kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ContextPRs(ctx context.Context, id contract.SessionID) ([]ContextPR, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT repo,number,approved_head!='' FROM pr_owners WHERE session=? ORDER BY repo,number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextPR
	for rows.Next() {
		var p ContextPR
		if err := rows.Scan(&p.Repo, &p.Number, &p.Approved); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ContextDepth(ctx context.Context, id contract.SessionID) (int, error) {
	var depth int
	err := s.Read().QueryRowContext(ctx, `WITH RECURSIVE parents(id,parent) AS (SELECT id,parent FROM sessions WHERE id=? UNION ALL SELECT s.id,s.parent FROM sessions s JOIN parents p ON s.id=p.parent) SELECT COUNT(*) FROM parents`, id).Scan(&depth)
	return depth, err
}

func (s *Store) SavedTurnSections(ctx context.Context, turn contract.TurnID) ([]contract.ContextSection, bool, error) {
	var data []byte
	if err := s.Read().QueryRowContext(ctx, "SELECT sections FROM turns WHERE id=?", turn).Scan(&data); err != nil {
		return nil, false, err
	}
	if data == nil {
		return nil, false, nil
	}
	var sections []contract.ContextSection
	err := json.Unmarshal(data, &sections)
	return sections, true, err
}

// TurnSections saves the first rendered snapshot and returns it on every replay.
func (s *Store) TurnSections(ctx context.Context, turn contract.TurnID, sections []contract.ContextSection) ([]contract.ContextSection, error) {
	data, err := json.Marshal(sections)
	if err != nil {
		return nil, err
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE turns SET sections=? WHERE id=? AND sections IS NULL", data, turn); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT sections FROM turns WHERE id=?", turn).Scan(&data)
	})
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &sections)
	return sections, err
}
