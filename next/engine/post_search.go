package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) PostSearch(ctx context.Context, caller contract.Principal, req contract.PostSearchRequest) (contract.PostSearchResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.PostSearchResponse{}, forbidden()
	}
	words := strings.Fields(req.Query)
	if len(req.Query) > 200 || len(words) == 0 || len(words) > 8 || req.Before < 0 || req.Limit < 0 || req.Limit > 50 {
		return contract.PostSearchResponse{}, invalid("invalid search query or range")
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
		return contract.PostSearchResponse{}, failure(err)
	}
	defer tx.Rollback()
	var result contract.PostSearchResponse
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM post_search JOIN events e ON e.seq=post_search.rowid WHERE post_search MATCH ? AND (?='' OR e.room=?)`, query, req.Room, req.Room).Scan(&result.Total)
	if err != nil {
		return result, failure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.seq,e.room,e.at,e.from_kind,e.from_id,e.body FROM post_search JOIN events e ON e.seq=post_search.rowid WHERE post_search MATCH ? AND (?='' OR e.room=?) AND (?=0 OR e.seq<?) ORDER BY e.seq DESC LIMIT ?`, query, req.Room, req.Room, req.Before, req.Before, req.Limit)
	if err != nil {
		return result, failure(err)
	}
	defer rows.Close()
	result.Posts = make([]contract.SearchPost, 0)
	for rows.Next() {
		var post contract.SearchPost
		var at int64
		var from string
		var body []byte
		if err = rows.Scan(&post.Seq, &post.Room, &at, &post.Author.Kind, &from, &body); err != nil {
			return result, failure(err)
		}
		post.Time = time.UnixMilli(at).UTC()
		switch post.Author.Kind {
		case contract.PrincipalSession:
			post.Author.Session = contract.SessionID(from)
		case contract.PrincipalOwner:
			post.Author.Device = contract.DeviceID(from)
		}
		var payload contract.PostCreated
		if err = json.Unmarshal(body, &payload); err != nil {
			return result, failure(err)
		}
		post.Text, post.Home = payload.Text, payload.Home
		result.Posts = append(result.Posts, post)
	}
	if err = rows.Err(); err != nil {
		return result, failure(err)
	}
	return result, nil
}
