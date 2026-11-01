package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptTurnRange is a turn's range in one file. End is nil for the last turn in that file.
type TranscriptTurnRange struct {
	Turn  contract.TurnID
	Path  string
	Start int64
	End   *int64
}

// LEGACY(v1-transfer): TranscriptPathIndexed, for transcript files from before the turn index. Remove after the transfer.
// TranscriptPathIndexed reports whether a path has any indexed turn starts.
func (s *Store) TranscriptPathIndexed(ctx context.Context, session contract.SessionID, path string) (bool, error) {
	var indexed bool
	err := s.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM transcript_turns WHERE session=? AND path=?)`, session, path).Scan(&indexed)
	return indexed, err
}

// TranscriptTurnRanges returns each file range for a turn, with the next start as its exclusive end.
func (s *Store) TranscriptTurnRanges(ctx context.Context, session contract.SessionID, turn contract.TurnID) ([]TranscriptTurnRange, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT t.turn,t.path,t.start,(SELECT min(n.start) FROM transcript_turns n WHERE n.session=t.session AND n.path=t.path AND n.start>t.start) FROM transcript_turns t WHERE t.session=? AND t.turn=? ORDER BY t.path,t.start`, session, turn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ranges []TranscriptTurnRange
	for rows.Next() {
		var r TranscriptTurnRange
		if err := rows.Scan(&r.Turn, &r.Path, &r.Start, &r.End); err != nil {
			return nil, err
		}
		ranges = append(ranges, r)
	}
	return ranges, rows.Err()
}

// TranscriptTurnsIntersecting returns turns intersecting the half-open append range [start,end).
func (s *Store) TranscriptTurnsIntersecting(ctx context.Context, session contract.SessionID, path string, start, end int64) ([]contract.TurnID, error) {
	if start >= end {
		return nil, nil
	}
	rows, err := s.Read().QueryContext(ctx, `SELECT turn FROM transcript_turns WHERE session=? AND path=? AND start<? AND (start>=? OR start=(SELECT max(start) FROM transcript_turns WHERE session=? AND path=? AND start<=?)) ORDER BY start`, session, path, end, start, session, path, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []contract.TurnID
	for rows.Next() {
		var turn contract.TurnID
		if err := rows.Scan(&turn); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

// TranscriptTurnMetadata returns a bounded newest-first page without loading sent posts.
func (s *Store) TranscriptTurnMetadata(ctx context.Context, session contract.SessionID, before, exactTurn contract.TurnID, limit int) ([]contract.SessionWorkTurn, error) {
	if before != "" {
		var cursor int64
		err := s.Read().QueryRowContext(ctx, `SELECT started_seq FROM turns WHERE id=? AND session=? AND started_seq IS NOT NULL`, before, session).Scan(&cursor)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidWorkCursor
		}
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.Read().QueryContext(ctx, `SELECT started_seq FROM turns WHERE session=? AND started_seq IS NOT NULL AND (?='' OR started_seq < (SELECT started_seq FROM turns WHERE id=? AND session=?)) AND (?='' OR id=?) ORDER BY started_seq DESC LIMIT ?`, session, before, before, session, exactTurn, exactTurn, limit)
	if err != nil {
		return nil, err
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			return nil, err
		}
		seqs = append(seqs, seq)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]contract.SessionWorkTurn, 0, len(seqs))
	for _, seq := range seqs {
		started, err := s.Event(ctx, seq)
		if err != nil {
			return nil, err
		}
		body := started.Body.(contract.TurnStarted)
		row := contract.SessionWorkTurn{Started: started, Inputs: []contract.Event{}, Posts: []contract.Event{}}
		for _, id := range body.Inputs {
			input, err := s.EventInRoom(ctx, started.Room, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			row.Inputs = append(row.Inputs, input)
		}
		var endSeq sql.NullInt64
		err = s.Read().QueryRowContext(ctx, `SELECT min(seq) FROM events WHERE room=? AND type='turn.ended' AND from_kind='session' AND from_id=? AND json_extract(CAST(body AS TEXT),'$.turn')=? AND seq>?`, started.Room, session, body.Turn, seq).Scan(&endSeq)
		if err != nil {
			return nil, err
		}
		if endSeq.Valid {
			end, err := s.Event(ctx, endSeq.Int64)
			if err != nil {
				return nil, err
			}
			row.Ended = &end
		}
		result = append(result, row)
	}
	return result, nil
}

const transcriptPostsQuery = ` FROM events e JOIN turns t ON t.session=? AND t.id=?
WHERE e.type='post.created' AND e.from_kind='session' AND e.from_id=t.session
AND e.seq>t.started_seq
AND NOT EXISTS (SELECT 1 FROM turns n WHERE n.session=t.session AND n.started_seq>t.started_seq AND n.started_seq<=e.seq)
AND NOT EXISTS (SELECT 1 FROM events ended WHERE ended.room=(SELECT room FROM sessions WHERE id=t.session) AND ended.type='turn.ended' AND ended.from_kind='session' AND ended.from_id=t.session AND json_extract(CAST(ended.body AS TEXT),'$.turn')=t.id AND ended.seq>t.started_seq AND ended.seq<=e.seq)`

// TranscriptTurnPost finds a session post made during a turn, including posts in other rooms.
func (s *Store) TranscriptTurnPost(ctx context.Context, session contract.SessionID, turn contract.TurnID, id contract.EventID) (contract.Event, bool, error) {
	var seq int64
	err := s.Read().QueryRowContext(ctx, `SELECT e.seq`+transcriptPostsQuery+` AND e.id=?`, session, turn, id).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.Event{}, false, nil
	}
	if err != nil {
		return contract.Event{}, false, err
	}
	event, err := s.Event(ctx, seq)
	return event, err == nil, err
}

// TranscriptCallPosts returns up to limit posts and the total for each requested call.
func (s *Store) TranscriptCallPosts(ctx context.Context, session contract.SessionID, turn contract.TurnID, callIDs []string, limit int, visit func(string, contract.Event, int)) error {
	filter := transcriptPostsQuery + ` AND json_extract(CAST(e.body AS TEXT),'$.toolCallId')=?`
	for _, callID := range callIDs {
		var total int
		if err := s.Read().QueryRowContext(ctx, `SELECT count(*)`+filter, session, turn, callID).Scan(&total); err != nil {
			return err
		}
		if total == 0 {
			continue
		}
		rows, err := s.Read().QueryContext(ctx, `SELECT e.seq,e.id,e.at,e.room,e.type,e.from_kind,e.from_id,e.body`+filter+` ORDER BY e.at,e.seq LIMIT ?`, session, turn, callID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			event, err := scanEvent(rows)
			if err != nil {
				rows.Close()
				return err
			}
			visit(callID, event, total)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// TranscriptSentPosts returns a bounded page ordered by event time and sequence.
func (s *Store) TranscriptSentPosts(ctx context.Context, session contract.SessionID, turn contract.TurnID, limit, offset int) ([]contract.Event, error) {
	query := `SELECT e.seq,e.id,e.at,e.room,e.type,e.from_kind,e.from_id,e.body` + transcriptPostsQuery
	rows, err := s.Read().QueryContext(ctx, query+` ORDER BY e.at,e.seq LIMIT ? OFFSET ?`, session, turn, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts := []contract.Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, event)
	}
	return posts, rows.Err()
}

// TranscriptSentPostsNewest returns a bounded newest-first page.
func (s *Store) TranscriptSentPostsNewest(ctx context.Context, session contract.SessionID, turn contract.TurnID, limit, offset int) ([]contract.Event, error) {
	query := `SELECT e.seq,e.id,e.at,e.room,e.type,e.from_kind,e.from_id,e.body` + transcriptPostsQuery
	rows, err := s.Read().QueryContext(ctx, query+` AND (?=0 OR e.seq<=?) ORDER BY e.seq DESC LIMIT ?`, session, turn, offset, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []contract.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, event)
	}
	return posts, rows.Err()
}

// LEGACY(v1-transfer): the Indexed field, read only by the transcript backfill. Remove after the transfer.
type TranscriptFile struct {
	Path    string
	Acked   int64
	Indexed bool
}

func (s *Store) TranscriptFiles(ctx context.Context, session contract.SessionID) ([]TranscriptFile, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT o.path,o.acked,EXISTS(SELECT 1 FROM transcript_turns t WHERE t.session=o.session AND t.path=o.path) FROM transcript_offsets o WHERE o.session=? ORDER BY o.path`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []TranscriptFile
	for rows.Next() {
		var f TranscriptFile
		if err := rows.Scan(&f.Path, &f.Acked, &f.Indexed); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *Store) TranscriptDeliveries(ctx context.Context, session contract.SessionID) (map[contract.DeliverySeq]contract.TurnID, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT seq,turn FROM deliveries WHERE to_kind=? AND to_id=? AND turn IS NOT NULL", contract.PrincipalSession, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[contract.DeliverySeq]contract.TurnID)
	for rows.Next() {
		var seq contract.DeliverySeq
		var turn contract.TurnID
		if err := rows.Scan(&seq, &turn); err != nil {
			return nil, err
		}
		result[seq] = turn
	}
	return result, rows.Err()
}

// TranscriptRoom finds a session's room, including a closed session in transcript history.
func (s *Store) TranscriptRoom(ctx context.Context, session contract.SessionID) (contract.RoomID, error) {
	var room contract.RoomID
	err := s.Read().QueryRowContext(ctx, "SELECT room FROM sessions WHERE id=?", session).Scan(&room)
	return room, err
}
