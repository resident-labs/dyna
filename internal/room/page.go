package room

import (
	"fmt"
	"sort"
	"strings"

	"dyna-agent/internal/listpage"
)

// MessagePage contains one bounded history page and whole-log unread metadata.
type MessagePage struct {
	Messages    []Message
	Offset      int64
	Earlier     int
	Newer       int
	Cursor      string
	Unread      int
	FirstUnread string
}

// PageFilter selects the posts a page lists and marks the unread ones, so one
// read decides both before it pages: a reader that lists one conversation never
// pages over posts it leaves out, and the page's counts and cursor name only the
// posts it lists.
type PageFilter struct {
	// Keep lists only the posts it accepts, and nil lists every post.
	Keep func(Message) bool
	// Unread marks a post the owner has not read, and nil marks none. It is
	// asked only about the posts Keep lists.
	Unread func(Message) bool
}

type pageDirection uint8

const (
	pageBefore pageDirection = iota
	pageAfter
	pageAround
)

// ReadPage selects an older page in append order. filter reads indexed values
// without changing them. Returned messages belong to the caller.
func (s *Store) ReadPage(roomID, before string, limit int, filter PageFilter) (MessagePage, error) {
	return s.readPage(roomID, before, limit, pageBefore, filter)
}

// ReadAfter selects posts after an exclusive cursor. An empty cursor starts
// at the beginning of the retained log.
func (s *Store) ReadAfter(roomID, after string, limit int, filter PageFilter) (MessagePage, error) {
	return s.readPage(roomID, after, limit, pageAfter, filter)
}

// ReadAround selects a bounded page containing the cursor post. Pages balance
// around it, using the remaining side when the cursor is near an edge.
func (s *Store) ReadAround(roomID, id string, limit int, filter PageFilter) (MessagePage, error) {
	if id == "" {
		return MessagePage{}, fmt.Errorf("feed cursor post is required")
	}
	return s.readPage(roomID, id, limit, pageAround, filter)
}

// Search limits keep one query's work small.
const (
	MaxSearchBytes = 200
	MaxSearchWords = 8
)

// SearchMessages returns the newest messages, at most limit, that keep
// accepts and that hold every query word, ignoring case. Each word may be in
// the text or in the sender's address. It copies the shared index under its
// lock and matches outside it, so a slow search never holds up other
// readers. more reports older matches beyond limit.
func (s *Store) SearchMessages(roomID, query string, limit int, keep func(Message) bool) (msgs []Message, more bool, err error) {
	words := strings.Fields(strings.ToLower(query))
	switch {
	case len(words) == 0:
		return nil, false, fmt.Errorf("search text is required")
	case len(query) > MaxSearchBytes || len(words) > MaxSearchWords:
		return nil, false, fmt.Errorf("search text is limited to %d words and %d bytes", MaxSearchWords, MaxSearchBytes)
	}
	limit = listpage.Limit(limit)
	var all []Message
	err = s.withMessageLog(roomID, func(log *messageLog) {
		all = make([]Message, 0, len(log.lines))
		for _, line := range log.lines {
			if line.msg.ID != "" {
				all = append(all, line.msg)
			}
		}
	})
	if err != nil {
		return nil, false, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		if (keep != nil && !keep(m)) || !holdsWords(m, words) {
			continue
		}
		if len(msgs) == limit {
			return msgs, true, nil
		}
		msgs = append(msgs, cloneMessage(m))
	}
	return msgs, false, nil
}

func holdsWords(m Message, words []string) bool {
	text, from := strings.ToLower(m.Text), strings.ToLower(m.From)
	for _, w := range words {
		if !strings.Contains(text, w) && !strings.Contains(from, w) {
			return false
		}
	}
	return true
}

func (s *Store) readPage(roomID, cursor string, limit int, direction pageDirection, filter PageFilter) (MessagePage, error) {
	limit = listpage.Limit(limit)
	out := MessagePage{}
	var cursorErr error
	err := s.withMessageLog(roomID, func(log *messageLog) {
		out.Offset = log.offset
		cursorLine := len(log.lines)
		if cursor != "" {
			var found bool
			cursorLine, found = log.byID[cursor]
			if !found {
				cursorErr = fmt.Errorf("feed cursor post is not in this room")
				return
			}
		}
		// Keep only the posts this reader lists, so paging, the page's counts and
		// its cursor all measure that set and never the whole log.
		lines := make([]int, 0, len(log.lines))
		for i, line := range log.lines {
			if line.msg.ID == "" {
				continue
			}
			if filter.Keep != nil && !filter.Keep(line.msg) {
				continue
			}
			lines = append(lines, i)
			if filter.Unread != nil && filter.Unread(line.msg) {
				out.Unread++
				if out.FirstUnread == "" {
					out.FirstUnread = line.msg.ID
				}
			}
		}
		start, end := 0, len(lines)
		rank := sort.SearchInts(lines, cursorLine)
		switch direction {
		case pageBefore:
			end = rank
			start = max(0, end-limit)
		case pageAfter:
			// Start at the first listed post after the cursor. The filter can leave
			// the cursor itself out, and then rank already names that post.
			if cursor != "" {
				start = rank
				if start < len(lines) && lines[start] == cursorLine {
					start++
				}
			}
			end = min(len(lines), start+limit)
		case pageAround:
			start = max(0, rank-limit/2)
			end = min(len(lines), start+limit)
			start = max(0, end-limit)
		}
		for _, line := range lines[start:end] {
			out.Messages = append(out.Messages, cloneMessage(log.lines[line].msg))
		}
		out.Earlier, out.Newer = start, len(lines)-end
		if out.Earlier > 0 && len(out.Messages) > 0 {
			out.Cursor = out.Messages[0].ID
		}
	})
	if err == nil {
		err = cursorErr
	}
	return out, err
}
