package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

var ErrInvalidWorkCursor = errors.New("invalid session work cursor")

// SessionWork returns complete turns, newest first. Sequence numbers, not clocks,
// join posts to turns, because multiple events can share a millisecond.
func (s *Store) SessionWork(ctx context.Context, session contract.SessionID, before contract.TurnID, limit int) ([]contract.SessionWorkTurn, bool, int, error) {
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session=? AND started_seq IS NOT NULL`, session).Scan(&total); err != nil {
		return nil, false, 0, err
	}
	if before != "" {
		var cursor int64
		if err := s.read.QueryRowContext(ctx, `SELECT started_seq FROM turns WHERE id=? AND session=? AND started_seq IS NOT NULL`, before, session).Scan(&cursor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, false, 0, ErrInvalidWorkCursor
			}
			return nil, false, 0, err
		}
	}
	rows, err := s.read.QueryContext(ctx, `SELECT started_seq FROM turns
 WHERE session=? AND started_seq IS NOT NULL AND (?='' OR started_seq < (SELECT started_seq FROM turns WHERE id=? AND session=?))
 ORDER BY started_seq DESC LIMIT ?`, session, before, before, session, limit+1)
	if err != nil {
		return nil, false, 0, err
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		if err = rows.Scan(&seq); err != nil {
			rows.Close()
			return nil, false, 0, err
		}
		seqs = append(seqs, seq)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, 0, err
	}
	more := len(seqs) > limit
	if more {
		seqs = seqs[:limit]
	}
	result := make([]contract.SessionWorkTurn, 0, len(seqs))
	for _, seq := range seqs {
		started, err := s.Event(ctx, seq)
		if err != nil {
			return nil, false, 0, err
		}
		body := started.Body.(contract.TurnStarted)
		row := contract.SessionWorkTurn{Started: started, Inputs: []contract.Event{}, Posts: []contract.Event{}}
		for _, id := range body.Inputs {
			input, err := s.EventInRoom(ctx, started.Room, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, false, 0, err
			}
			row.Inputs = append(row.Inputs, input)
		}
		var endSeq sql.NullInt64
		err = s.read.QueryRowContext(ctx, `SELECT min(seq) FROM events WHERE room=? AND type='turn.ended' AND from_kind='session' AND from_id=? AND json_extract(CAST(body AS TEXT),'$.turn')=? AND seq>?`, started.Room, session, body.Turn, seq).Scan(&endSeq)
		if err != nil {
			return nil, false, 0, err
		}
		if endSeq.Valid {
			end, err := s.Event(ctx, endSeq.Int64)
			if err != nil {
				return nil, false, 0, err
			}
			row.Ended = &end
		}
		var nextSeq sql.NullInt64
		if err := s.read.QueryRowContext(ctx, `SELECT min(started_seq) FROM turns WHERE session=? AND started_seq>?`, session, seq).Scan(&nextSeq); err != nil {
			return nil, false, 0, err
		}
		bound := endSeq.Int64
		if nextSeq.Valid && (bound == 0 || nextSeq.Int64 < bound) {
			bound = nextSeq.Int64
		}
		posts, err := s.read.QueryContext(ctx, `SELECT seq FROM events WHERE room=? AND type='post.created' AND from_kind='session' AND from_id=? AND seq>? AND (?=0 OR seq<?) ORDER BY seq`, started.Room, session, seq, bound, bound)
		if err != nil {
			return nil, false, 0, err
		}
		var postSeqs []int64
		for posts.Next() {
			var postSeq int64
			if err = posts.Scan(&postSeq); err != nil {
				posts.Close()
				return nil, false, 0, err
			}
			postSeqs = append(postSeqs, postSeq)
		}
		err = posts.Err()
		posts.Close()
		if err != nil {
			return nil, false, 0, err
		}
		for _, postSeq := range postSeqs {
			post, err := s.Event(ctx, postSeq)
			if err != nil {
				return nil, false, 0, err
			}
			row.Posts = append(row.Posts, post)
		}
		result = append(result, row)
	}
	return result, more, total, nil
}
