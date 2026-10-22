package room

import (
	"slices"
	"sort"
	"time"
)

// MessageSelection remembers matching IDs and the position inspected in one
// generation of the shared log index. Its caller must serialize access.
type MessageSelection struct {
	version *messageLogVersion
	offset  int
	ids     []string
	lines   []int
	byTime  []selectedMessageTime
}

type selectedMessageTime struct {
	line, position int
	ts             int64
}

// MessageIDs selects IDs in append order. The index owns file validity and
// resets the selection when it rebuilds. match runs only for new lines, after
// the index lock is released, so it may read other posts from the store.
func (s *Store) MessageIDs(roomID string, selection *MessageSelection, match func(Message) bool) ([]string, error) {
	type indexedMessage struct {
		message Message
		line    int
	}
	var added []indexedMessage
	var version *messageLogVersion
	var offset int
	reset := false
	err := s.withMessageLog(roomID, func(log *messageLog) {
		version = log.version
		offset = len(log.lines)
		reset = selection.version != version
		start := selection.offset
		if reset {
			start = 0
		}
		for i := start; i < len(log.lines); i++ {
			if line := log.lines[i]; line.msg.ID != "" {
				added = append(added, indexedMessage{cloneMessage(line.msg), i})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if reset {
		selection.ids, selection.lines, selection.byTime = nil, nil, nil
	}
	for _, added := range added {
		if match(added.message) {
			selection.ids = append(selection.ids, added.message.ID)
			selection.lines = append(selection.lines, added.line)
		}
	}
	selection.version, selection.offset = version, offset
	return selection.ids, nil
}

// SelectedMessages returns the newest matching messages in an inclusive time
// window, preserving append order. Zero bounds include all retained time.
func (s *Store) SelectedMessages(roomID string, selection *MessageSelection, match func(Message) bool, since, before time.Time, limit int) ([]Message, error) {
	return s.selectedMessages(roomID, selection, match, since, before, limit, false)
}

// LatestSelectedMessages returns the newest matching messages by timestamp,
// with append order breaking ties. The result is oldest first.
func (s *Store) LatestSelectedMessages(roomID string, selection *MessageSelection, match func(Message) bool, limit int) ([]Message, error) {
	return s.selectedMessages(roomID, selection, match, time.Time{}, time.Time{}, limit, true)
}

func (s *Store) selectedMessages(roomID string, selection *MessageSelection, match func(Message) bool, since, before time.Time, limit int, latest bool) ([]Message, error) {
	for {
		if _, err := s.MessageIDs(roomID, selection, match); err != nil {
			return nil, err
		}
		var out []Message
		replaced := false
		err := s.withMessageLog(roomID, func(log *messageLog) {
			if log.version != selection.version {
				replaced = true
				return
			}
			if limit <= 0 {
				return
			}
			// ID-only readers do not need the time index.
			for position := len(selection.byTime); position < len(selection.lines); position++ {
				line := selection.lines[position]
				entry := selectedMessageTime{line: line, position: position, ts: log.lines[line].msg.TS}
				i := sort.Search(len(selection.byTime), func(i int) bool { return selection.byTime[i].ts > entry.ts })
				selection.byTime = slices.Insert(selection.byTime, i, entry)
			}
			start, end := 0, len(selection.byTime)
			if !since.IsZero() {
				start = sort.Search(end, func(i int) bool { return !time.UnixMilli(selection.byTime[i].ts).Before(since) })
			}
			if !before.IsZero() {
				end = sort.Search(end, func(i int) bool { return time.UnixMilli(selection.byTime[i].ts).After(before) })
			}
			if end <= start {
				return
			}
			copyLine := func(line int) {
				out = append(out, cloneMessage(log.lines[line].msg))
			}
			if latest {
				for _, entry := range selection.byTime[max(start, end-limit):end] {
					copyLine(entry.line)
				}
			} else if start == 0 && end == len(selection.ids) {
				for _, line := range selection.lines[max(0, end-limit):end] {
					copyLine(line)
				}
			} else {
				positions := make([]int, 0, end-start)
				for _, entry := range selection.byTime[start:end] {
					positions = append(positions, entry.position)
				}
				slices.Sort(positions)
				for _, position := range positions[max(0, len(positions)-limit):] {
					copyLine(selection.lines[position])
				}
			}
		})
		if err != nil || !replaced {
			return out, err
		}
		// A replacement between refresh and selection belongs to a new index.
	}
}
