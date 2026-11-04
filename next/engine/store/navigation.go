package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// PostHitQuery is one search of post text. Match is an FTS5 expression. Room narrows to one room; the empty room is all.
// DMs are returned only from DMRoom, or from every room when AllDMs is set. From is an exact author name, or owner.
// Before, when above zero, keeps only older posts.
type PostHitQuery struct {
	Match  string
	Room   contract.RoomID
	DMRoom contract.RoomID
	AllDMs bool
	From   string
	Before int64
	Limit  int
}

type PostHit struct {
	Seq      int64
	ID       contract.EventID
	RoomName string
	At       time.Time
	Author   string
	DM       string // the DM session's name, or empty for the feed
	Snippet  string
	Source   *contract.PostSource
	Files    int
}

const postHitFrom = ` FROM post_search JOIN events e ON e.seq=post_search.rowid JOIN rooms r ON r.id=e.room LEFT JOIN sessions s ON e.from_kind='session' AND s.id=e.from_id LEFT JOIN sessions h ON h.id=e.home WHERE post_search MATCH ? AND (?='' OR e.room=?) AND (?=1 OR e.home IS NULL OR e.room=?) AND (?='' OR CASE e.from_kind WHEN 'session' THEN COALESCE(s.name,'') ELSE e.from_kind END=?)`

// SearchPostHits reads the total and one page, newest first, from one snapshot. The count and the page share one filter,
// so a DM the caller may not read is in neither.
func (s *Store) SearchPostHits(ctx context.Context, q PostHitQuery) ([]PostHit, int, error) {
	tx, err := s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	all := 0
	if q.AllDMs {
		all = 1
	}
	args := []any{q.Match, q.Room, q.Room, all, q.DMRoom, q.From, q.From}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*)`+postHitFrom, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.seq,e.id,r.name,e.at,e.from_kind,COALESCE(s.name,''),COALESCE(h.name,''),snippet(post_search,0,'[',']','…',24),e.body`+postHitFrom+` AND (?=0 OR e.seq<?) ORDER BY e.seq DESC LIMIT ?`, append(args, q.Before, q.Before, q.Limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []PostHit
	for rows.Next() {
		var hit PostHit
		var at int64
		var kind, name string
		var body []byte
		if err = rows.Scan(&hit.Seq, &hit.ID, &hit.RoomName, &at, &kind, &name, &hit.DM, &hit.Snippet, &body); err != nil {
			return nil, 0, err
		}
		hit.At = time.UnixMilli(at).UTC()
		hit.Author = kind
		if kind == string(contract.PrincipalSession) {
			hit.Author = name
		}
		var payload contract.PostCreated
		if err = json.Unmarshal(body, &payload); err != nil {
			return nil, 0, err
		}
		hit.Source, hit.Files = payload.Source, len(payload.Attachments)
		hits = append(hits, hit)
	}
	return hits, total, rows.Err()
}

// likeAll builds a clause that needs each word in one of the columns, and its arguments.
func likeAll(words []string, columns ...string) (string, []any) {
	var clauses []string
	var args []any
	for _, word := range words {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(word) + "%"
		var parts []string
		for _, column := range columns {
			parts = append(parts, column+` LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "1", nil
	}
	return strings.Join(clauses, " AND "), args
}

// SearchSessionHits finds sessions of one room, or of every room when room is empty, whose name or latest goal has each word.
// Open ones come first. Total counts every match.
func (s *Store) SearchSessionHits(ctx context.Context, words []string, room contract.RoomID, offset, limit int) ([]contract.SessionHit, int, error) {
	clause, args := likeAll(words, "s.name", "goal")
	args = append([]any{room, room}, args...)
	rows, err := s.Read().QueryContext(ctx, `SELECT id,name,room,parent,state,goal,COUNT(*) OVER () FROM (SELECT s.id AS id,s.name AS name,r.name AS room,COALESCE(p.name,'') AS parent,
CASE WHEN s.closed_at IS NOT NULL THEN 'closed'
 WHEN EXISTS (SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL) THEN 'working'
 WHEN (SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND outcome!='stopped' ORDER BY ended_at DESC LIMIT 1)='failed' OR EXISTS (SELECT 1 FROM boxes b WHERE b.session=s.id AND b.state IN ('failed','lost')) THEN 'failed'
 ELSE 'open' END AS state,
COALESCE((SELECT g.text FROM goals g WHERE g.session=s.id ORDER BY g.updated_at DESC,g.number DESC LIMIT 1),'') AS goal,
s.closed_at IS NOT NULL AS closed,s.created_at AS created,s.rowid AS row
FROM sessions s JOIN rooms r ON r.id=s.room LEFT JOIN sessions p ON p.id=s.parent WHERE (?='' OR s.room=?)) s WHERE `+clause+` ORDER BY closed,created DESC,row DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []contract.SessionHit
	total := 0
	for rows.Next() {
		var hit contract.SessionHit
		if err = rows.Scan(&hit.ID, &hit.Name, &hit.Room, &hit.Parent, &hit.State, &hit.Goal, &total); err != nil {
			return nil, 0, err
		}
		hits = append(hits, hit)
	}
	return hits, total, rows.Err()
}

// SearchRoomHits lists the rooms whose name has each word, by name. LastPost is the newest feed post, never a DM.
func (s *Store) SearchRoomHits(ctx context.Context, words []string, room contract.RoomID, offset, limit int) ([]contract.RoomHit, int, error) {
	clause, args := likeAll(words, "name")
	args = append([]any{room, room}, args...)
	rows, err := s.Read().QueryContext(ctx, `SELECT id,name,root,open,working,last,COUNT(*) OVER () FROM (SELECT r.id AS id,r.name AS name,
COALESCE((SELECT name FROM sessions WHERE room=r.id AND parent IS NULL AND closed_at IS NULL LIMIT 1),'') AS root,
(SELECT count(*) FROM sessions WHERE room=r.id AND closed_at IS NULL) AS open,
(SELECT count(*) FROM sessions x WHERE x.room=r.id AND x.closed_at IS NULL AND EXISTS (SELECT 1 FROM turns t WHERE t.session=x.id AND t.ended_at IS NULL)) AS working,
COALESCE((SELECT max(at) FROM events WHERE room=r.id AND type='post.created' AND home IS NULL),0) AS last FROM rooms r WHERE r.state='open' AND (?='' OR r.id=?)) WHERE `+clause+` ORDER BY name LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []contract.RoomHit
	total := 0
	for rows.Next() {
		var hit contract.RoomHit
		var last int64
		if err = rows.Scan(&hit.ID, &hit.Name, &hit.Root, &hit.Open, &hit.Working, &last, &total); err != nil {
			return nil, 0, err
		}
		if last > 0 {
			hit.LastPost = time.UnixMilli(last).UTC()
		}
		hits = append(hits, hit)
	}
	return hits, total, rows.Err()
}

// ResolveRoom finds a room by name or ID; the error is sql.ErrNoRows for none.
func (s *Store) ResolveRoom(ctx context.Context, ref string) (contract.RoomID, string, error) {
	var id contract.RoomID
	var name string
	err := s.Read().QueryRowContext(ctx, "SELECT id,name FROM rooms WHERE (id=? OR name=?) AND state='open' ORDER BY id=? DESC LIMIT 1", ref, ref, ref).Scan(&id, &name)
	return id, name, err
}

// RoomNameOf returns a room's name.
func (s *Store) RoomNameOf(ctx context.Context, id contract.RoomID) (string, error) {
	var name string
	err := s.Read().QueryRowContext(ctx, "SELECT name FROM rooms WHERE id=?", id).Scan(&name)
	return name, err
}

// PostPlace says where a post is: its room and, for a DM post, the DM's session.
type PostPlace struct {
	Seq  int64
	Room contract.RoomID
	DM   contract.SessionID
}

// PostPlaceOf finds a post by ID in any room; the error is sql.ErrNoRows for none.
func (s *Store) PostPlaceOf(ctx context.Context, id contract.EventID) (PostPlace, error) {
	var place PostPlace
	var dm sql.NullString
	err := s.Read().QueryRowContext(ctx, "SELECT seq,room,home FROM events WHERE id=? AND type='post.created'", id).Scan(&place.Seq, &place.Room, &dm)
	place.DM = contract.SessionID(dm.String)
	return place, err
}

const postConversation = `type='post.created' AND room=? AND ((?='' AND home IS NULL) OR home=?)`

// PostsPage reads posts of one conversation, a room's feed when dm is empty. After above zero reads the posts after it in
// order; otherwise the newest below before (or the newest of all when before is zero), returned in order. Each page holds
// at most limit posts.
func (s *Store) PostsPage(ctx context.Context, room contract.RoomID, dm contract.SessionID, after, before int64, limit int) ([]contract.Event, error) {
	query := "SELECT seq,id,at,room,type,from_kind,from_id,body FROM events WHERE " + postConversation
	args := []any{room, dm, dm}
	switch {
	case after > 0:
		query += " AND seq>? ORDER BY seq LIMIT ?"
		args = append(args, after, limit)
	default:
		query += " AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?"
		args = append(args, before, before, limit)
	}
	rows, err := s.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []contract.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if after == 0 {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	return events, rows.Err()
}

// PostBeyond reports whether the conversation has a post older than seq, or newer when newer is set.
func (s *Store) PostBeyond(ctx context.Context, room contract.RoomID, dm contract.SessionID, seq int64, newer bool) (bool, error) {
	op := "<"
	if newer {
		op = ">"
	}
	var found int
	err := s.Read().QueryRowContext(ctx, "SELECT 1 FROM events WHERE "+postConversation+" AND seq"+op+"? LIMIT 1", room, dm, dm, seq).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SessionInRoom finds a session of room by ID or name, the newest if a name was used again; closed ones are found.
// The error is sql.ErrNoRows for none.
func (s *Store) SessionInRoom(ctx context.Context, room contract.RoomID, ref string) (contract.SessionID, error) {
	var id contract.SessionID
	err := s.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND (id=? OR name=?) ORDER BY id=? DESC,closed_at IS NOT NULL,created_at DESC LIMIT 1", room, ref, ref, ref).Scan(&id)
	return id, err
}
