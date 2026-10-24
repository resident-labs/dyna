package room

import (
	"database/sql"
	"math"
	"slices"
	"sort"
	"time"
)

// The conversation reads answer from post_homes and post_deliveries, the rows
// the write path keeps (see postindex.go), so a reader never works out a
// conversation from the address strings of a post. Post bodies stay in the
// room's JSONL log: the rows name the posts, and the log holds them.
//
// A post stored before the rows existed has them anyway: room.Migrate imports
// the retained posts of every room, and the engine runs it before it serves
// any call (see internal/engine/stores).

// HomePost is one stored post with the home its index row names.
type HomePost struct {
	Message Message
	Home    Conversation
}

const (
	// homeColumns is the select list of every conversation read: the post and
	// the conversation it was written in.
	homeColumns = `h.post_id, h.conversation`
	// homeOrder lists the newest posts first, so a bounded read keeps the
	// newest ones. post_homes.rowid breaks a tie in append order.
	homeOrder = ` ORDER BY h.ts DESC, h.rowid DESC`
)

// ConversationMessages returns the posts whose home is conversation, oldest
// last. A zero since or before leaves that side of the window open, and a
// limit of zero keeps every post.
func (s *Store) ConversationMessages(roomID string, conversation Conversation, since, before time.Time, limit int) ([]Message, error) {
	posts, err := s.readHomes(roomID, `WHERE h.room_id=? AND h.conversation=?`, []any{roomID, conversation.Home().String()}, since, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(posts))
	for _, post := range posts {
		out = append(out, post.Message)
	}
	return out, nil
}

// ReceivedPosts returns the posts delivered to recipient (a session ID, or a
// root seat, see SeatSessionID) whose home is not conversation: the posts one
// session reads from another conversation. A delivery that was cancelled or
// failed never reached the recipient, so it is left out. A zero since or
// before leaves that side of the window open, and a limit of zero keeps every
// post.
func (s *Store) ReceivedPosts(roomID, recipient string, conversation Conversation, since, before time.Time, limit int) ([]HomePost, error) {
	return s.readHomes(roomID, `JOIN post_deliveries d ON d.room_id=h.room_id AND d.post_id=h.post_id
 WHERE d.room_id=? AND d.session_id=? AND h.conversation<>? AND d.state NOT IN ('cancelled','failed')`,
		[]any{roomID, recipient, conversation.Home().String()}, since, before, limit)
}

// AuthoredPosts returns the posts author wrote whose home is not conversation:
// what one session said in the other conversations it spoke in. A zero since or
// before leaves that side of the window open, and a limit of zero keeps every
// post.
func (s *Store) AuthoredPosts(roomID, author string, conversation Conversation, since, before time.Time, limit int) ([]HomePost, error) {
	if author == "" {
		return nil, nil
	}
	return s.readHomes(roomID, `WHERE h.room_id=? AND h.author_session_id=? AND h.conversation<>?`,
		[]any{roomID, author, conversation.Home().String()}, since, before, limit)
}

// HasDeliveryFrom reports whether author wrote a post in turn with a live
// delivery row to recipient and one of kinds. An empty kinds accepts every
// kind. The index rows decide identity; the stored post decides turn and kind.
func (s *Store) HasDeliveryFrom(roomID, author, recipient string, turn int, kinds ...string) (bool, error) {
	if author == "" || recipient == "" {
		return false, nil
	}
	posts, err := s.readHomes(roomID, `JOIN post_deliveries d ON d.room_id=h.room_id AND d.post_id=h.post_id
 WHERE h.room_id=? AND h.author_session_id=? AND d.room_id=? AND d.session_id=? AND d.state NOT IN ('cancelled','failed')`,
		[]any{roomID, author, roomID, recipient}, time.Time{}, time.Time{}, 0)
	if err != nil {
		return false, err
	}
	for _, post := range posts {
		if post.Message.Turn == turn && (len(kinds) == 0 || slices.Contains(kinds, post.Message.Kind)) {
			return true, nil
		}
	}
	return false, nil
}

// OtherHomes returns the home of every post of the room that was not written in
// the room feed: the owner's DMs and the owner's voice call, by post ID. A post
// the map does not name was written in the room feed. A reader that lists the
// room feed asks this once, then reads each post's home from the map.
func (s *Store) OtherHomes(roomID string) (map[string]Conversation, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT post_id, conversation FROM post_homes WHERE room_id=? AND conversation<>?`,
		roomID, ConversationRoom.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Conversation{}
	for rows.Next() {
		var id, home string
		if err := rows.Scan(&id, &home); err != nil {
			return nil, err
		}
		out[id] = Conversation(home)
	}
	return out, rows.Err()
}

// HomeOf is the stored home of one post: its index row, or the conversation the
// post carries when the index has no row for it yet. A post that names neither
// is the room feed.
func (s *Store) HomeOf(roomID, postID string) Conversation {
	var db *sql.DB
	if opened, err := s.postIndexDB(); err == nil {
		db = opened
	}
	return s.ReplyHome(db, roomID, postID).Home()
}

// readHomes runs one conversation read: from names the part of the query after
// post_homes, such as a WHERE clause or a join. The rows it selects are read
// oldest last.
func (s *Store) readHomes(roomID, from string, args []any, since, before time.Time, limit int) ([]HomePost, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	query := "SELECT " + homeColumns + " FROM post_homes h " + from
	if !since.IsZero() {
		query += " AND h.ts>=?"
		args = append(args, since.UnixMilli())
	}
	if !before.IsZero() {
		query += " AND h.ts<=?"
		args = append(args, before.UnixMilli())
	}
	query += homeOrder
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var posts []HomePost
	var ids []string
	for rows.Next() {
		var post HomePost
		var home string
		if err := rows.Scan(&post.Message.ID, &home); err != nil {
			rows.Close()
			return nil, err
		}
		post.Home = Conversation(home)
		posts = append(posts, post)
		ids = append(ids, post.Message.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The query lists the newest first; a reader draws a conversation oldest
	// last, as the log does.
	messages, err := s.messagesByID(roomID, ids, false)
	if err != nil {
		return nil, err
	}
	out := make([]HomePost, 0, len(posts))
	for i := len(posts) - 1; i >= 0; i-- {
		// The room no longer holds a post the rows name, so no reader shows it.
		if m, ok := messages[posts[i].Message.ID]; ok {
			out = append(out, HomePost{Message: m, Home: posts[i].Home})
		}
	}
	return out, nil
}

// fileBatch is how many rows one step of Files reads. Files are sparse, so a
// long conversation usually needs a few steps only.
const fileBatch = 256

// Files returns the files of one DM, newest last: the posts with attachments
// that were written in conversation, delivered to recipient, or authored by
// author outside it. It reads the rows in batches and stops when it has limit
// posts, so a long conversation costs no copy of every post.
func (s *Store) Files(roomID string, conversation Conversation, recipient, author string, limit int) ([]Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	home := conversation.Home().String()
	type source struct {
		from string
		args []any
	}
	sources := []source{
		{`WHERE h.room_id=? AND h.conversation=?`, []any{roomID, home}},
		{`JOIN post_deliveries d ON d.room_id=h.room_id AND d.post_id=h.post_id
 WHERE d.room_id=? AND d.session_id=? AND h.conversation<>? AND d.state NOT IN ('cancelled','failed')`,
			[]any{roomID, recipient, home}},
	}
	if author != "" {
		sources = append(sources, source{`WHERE h.room_id=? AND h.author_session_id=? AND h.conversation<>?`, []any{roomID, author, home}})
	}
	seen := map[string]bool{}
	var out []Message
	for _, source := range sources {
		files, err := s.homeFiles(roomID, source.from, source.args, limit)
		if err != nil {
			return nil, err
		}
		for _, m := range files {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// homeFiles is Files for one source: it walks that source's rows newest first
// in batches until it has limit posts with files.
func (s *Store) homeFiles(roomID, from string, args []any, limit int) ([]Message, error) {
	db, err := s.postIndexDB()
	if err != nil {
		return nil, err
	}
	// The cursor is the (ts, rowid) of the last row read, which is the order the
	// rows are listed in.
	query := "SELECT h.post_id, h.ts, h.rowid FROM post_homes h " + from +
		" AND (h.ts<? OR (h.ts=? AND h.rowid<?)) ORDER BY h.ts DESC, h.rowid DESC LIMIT ?"
	ts, row := int64(math.MaxInt64), int64(math.MaxInt64)
	var out []Message
	for len(out) < limit {
		rows, err := db.Query(query, append(append([]any{}, args...), ts, ts, row, fileBatch)...)
		if err != nil {
			return nil, err
		}
		var ids []string
		read := 0
		for rows.Next() {
			var id string
			if err := rows.Scan(&id, &ts, &row); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
			read++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		messages, err := s.messagesByID(roomID, ids, true)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if m, ok := messages[id]; ok {
				out = append(out, m)
			}
		}
		if read < fileBatch {
			break
		}
	}
	return out, nil
}

// messagesByID reads the stored posts with these IDs in one pass of the log
// index. A post the log no longer holds is left out, and filesOnly keeps only
// the posts that carry files, without copying the others.
func (s *Store) messagesByID(roomID string, ids []string, filesOnly bool) (map[string]Message, error) {
	out := make(map[string]Message, len(ids))
	err := s.withMessageLog(roomID, func(log *messageLog) {
		for _, id := range ids {
			i, ok := log.byID[id]
			if !ok {
				continue
			}
			if filesOnly && len(log.lines[i].msg.Attachments) == 0 {
				continue
			}
			out[id] = cloneMessage(log.lines[i].msg)
		}
	})
	return out, err
}
