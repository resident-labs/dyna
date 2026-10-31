package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// SearchSessions reads the count and page from one consistent snapshot.
func (s *Store) SearchSessions(ctx context.Context, req contract.SessionSearchRequest, query string) (contract.SessionSearchResponse, error) {
	tx, err := s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return contract.SessionSearchResponse{}, err
	}
	defer tx.Rollback()
	result := contract.SessionSearchResponse{Sessions: []contract.SearchSession{}}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM session_search WHERE session_search MATCH ?`, query).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,s.room,s.closed_at IS NOT NULL FROM session_search JOIN sessions s ON s.rowid=session_search.rowid WHERE session_search MATCH ? ORDER BY s.closed_at IS NOT NULL,s.created_at DESC,s.rowid DESC LIMIT ?`, query, req.Limit)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item contract.SearchSession
		if err = rows.Scan(&item.ID, &item.Name, &item.Room, &item.Closed); err != nil {
			return result, err
		}
		result.Sessions = append(result.Sessions, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}
