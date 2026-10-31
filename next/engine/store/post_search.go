package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// SearchPosts reads the count and page from one consistent snapshot.
func (s *Store) SearchPosts(ctx context.Context, req contract.PostSearchRequest, query string) (contract.PostSearchResponse, error) {
	tx, err := s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return contract.PostSearchResponse{}, err
	}
	defer tx.Rollback()
	var result contract.PostSearchResponse
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM post_search JOIN events e ON e.seq=post_search.rowid WHERE post_search MATCH ? AND (?='' OR e.room=?)`, query, req.Room, req.Room).Scan(&result.Total)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.seq,e.room,e.at,e.from_kind,e.from_id,e.body,COALESCE(s.name,'') FROM post_search JOIN events e ON e.seq=post_search.rowid LEFT JOIN sessions s ON e.from_kind='session' AND s.id=e.from_id WHERE post_search MATCH ? AND (?='' OR e.room=?) AND (?=0 OR e.seq<?) ORDER BY e.seq DESC LIMIT ?`, query, req.Room, req.Room, req.Before, req.Before, req.Limit)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Posts = make([]contract.SearchPost, 0)
	result.Names = map[contract.SessionID]string{}
	for rows.Next() {
		var post contract.SearchPost
		var at int64
		var from, name string
		var body []byte
		if err = rows.Scan(&post.Seq, &post.Room, &at, &post.Author.Kind, &from, &body, &name); err != nil {
			return result, err
		}
		post.Time = time.UnixMilli(at).UTC()
		switch post.Author.Kind {
		case contract.PrincipalSession:
			post.Author.Session = contract.SessionID(from)
			if name != "" {
				result.Names[post.Author.Session] = name
			}
		case contract.PrincipalOwner:
			post.Author.Device = contract.DeviceID(from)
		}
		var payload contract.PostCreated
		if err = json.Unmarshal(body, &payload); err != nil {
			return result, err
		}
		post.Text, post.Home = payload.Text, payload.Home
		result.Posts = append(result.Posts, post)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, nil
}
