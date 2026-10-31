package engine

import (
	"context"
	"database/sql"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) SessionSearch(ctx context.Context, caller contract.Principal, req contract.SessionSearchRequest) (contract.SessionSearchResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.SessionSearchResponse{}, forbidden()
	}
	words := strings.Fields(req.Query)
	if len(req.Query) > 200 || len(words) == 0 || len(words) > 8 || req.Limit < 0 || req.Limit > 50 {
		return contract.SessionSearchResponse{}, invalid("invalid search query or range")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"*`
	}
	query := strings.Join(words, " AND ")
	tx, err := s.store.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return contract.SessionSearchResponse{}, failure(err)
	}
	defer tx.Rollback()
	result := contract.SessionSearchResponse{Sessions: []contract.SearchSession{}}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM session_search WHERE session_search MATCH ?`, query).Scan(&result.Total); err != nil {
		return result, failure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,s.room,s.closed_at IS NOT NULL FROM session_search JOIN sessions s ON s.rowid=session_search.rowid WHERE session_search MATCH ? ORDER BY s.closed_at IS NOT NULL,s.created_at DESC,s.rowid DESC LIMIT ?`, query, req.Limit)
	if err != nil {
		return result, failure(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item contract.SearchSession
		if err = rows.Scan(&item.ID, &item.Name, &item.Room, &item.Closed); err != nil {
			return result, failure(err)
		}
		result.Sessions = append(result.Sessions, item)
	}
	if err = rows.Err(); err != nil {
		return result, failure(err)
	}
	return result, nil
}
