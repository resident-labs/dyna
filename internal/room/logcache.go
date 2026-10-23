package room

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"dyna-agent/internal/store"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"time"
)

type messageLine struct {
	start, end int64
	msg        Message
}

// A nonzero-size token identifies one index generation without retaining its rows.
type messageLogVersion byte

type messageLog struct {
	version            *messageLogVersion
	projectionRevision int64
	file               *os.File
	info               os.FileInfo
	offset             int64
	lines              []messageLine
	byID               map[string]int
}

// withMessageLog owns the shared index for every room log reader. Files are
// append-only; replacement, truncation and same-size edits rebuild the index.
func (s *Store) withMessageLog(roomID string, use func(*messageLog)) error {
	path, err := s.messagesPath(roomID)
	if err != nil {
		return err
	}
	revision, err := s.postNormalizationRevision()
	if err != nil {
		return err
	}

	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logs == nil {
		s.logs = map[string]*messageLog{}
	}
	info, err := os.Stat(path)
	if err != nil {
		s.dropMessageLog(path)
		if os.IsNotExist(err) {
			if _, err := s.readRecord(roomID); err != nil {
				return err
			}
			use(&messageLog{})
			return nil
		}
		return err
	}
	log := s.logs[path]
	if log != nil && (log.projectionRevision != revision || !os.SameFile(log.info, info) || info.Size() < log.info.Size() || (info.Size() == log.info.Size() && !store.VersionOf(info).Equal(store.VersionOf(log.info)))) {
		s.dropMessageLog(path)
		log = nil
	}
	if log == nil {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		current, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		info = current
		// Keep the inode open while indexed. Otherwise a rapid replacement can
		// reuse it and look like an append when the replacement is larger.
		log = &messageLog{version: new(messageLogVersion), projectionRevision: revision, file: f, byID: map[string]int{}}
		s.logs[path] = log
	}
	if log.info == nil || !store.VersionOf(info).Equal(store.VersionOf(log.info)) {
		if _, err := log.file.Seek(log.offset, io.SeekStart); err != nil {
			s.dropMessageLog(path)
			return err
		}
		next, err := s.scanMessageLines(roomID, io.LimitReader(log.file, info.Size()-log.offset), log.offset, func(entry messageLine) {
			if entry.msg.ID != "" {
				if _, exists := log.byID[entry.msg.ID]; !exists {
					log.byID[entry.msg.ID] = len(log.lines)
				}
			}
			log.lines = append(log.lines, entry)
		})
		if err != nil {
			s.dropMessageLog(path)
			return err
		}
		log.offset = next
	}
	log.info = info
	s.logs[path] = log
	use(log)
	return nil
}

// dropMessageLog runs under logMu. The retained file also closes when its
// Store becomes unreachable, through os.File's normal resource cleanup.
func (s *Store) dropMessageLog(path string) {
	if log := s.logs[path]; log != nil {
		_ = log.file.Close()
	}
	delete(s.logs, path)
}

// ReadFrom returns complete messages in the next 4 MiB of the log. Readers
// share decoded records, but receive independent values they can change.
func (s *Store) ReadFrom(roomID string, offset int64) ([]Message, int64, error) {
	var out []Message
	next := offset
	boundary := true
	err := s.withMessageLog(roomID, func(log *messageLog) {
		if log.info == nil {
			return
		}
		if offset > log.info.Size() {
			offset = 0
			next = 0
		}
		i := sort.Search(len(log.lines), func(i int) bool { return log.lines[i].start >= offset })
		if offset < 0 || (i < len(log.lines) && log.lines[i].start != offset) || (i == len(log.lines) && offset != log.offset) {
			boundary = false
			return
		}
		for ; i < len(log.lines); i++ {
			line := log.lines[i]
			if line.end > offset+4<<20 {
				break
			}
			next = line.end
			if line.msg.ID != "" {
				out = append(out, cloneMessage(line.msg))
			}
		}
	})
	if err == nil && !boundary {
		return s.readFromUncached(roomID, offset)
	}
	return out, next, err
}

// OrderMessages sorts stored posts in place by their room-log append order.
// It uses the shared index without copying unrelated retained messages.
func (s *Store) OrderMessages(roomID string, msgs []Message) error {
	return s.withMessageLog(roomID, func(log *messageLog) {
		sort.SliceStable(msgs, func(i, j int) bool {
			return log.byID[msgs[i].ID] < log.byID[msgs[j].ID]
		})
	})
}

func cloneMessage(m Message) Message {
	m.SessionIDs = maps.Clone(m.SessionIDs)
	m.To = slices.Clone(m.To)
	m.Via = slices.Clone(m.Via)
	m.Attachments = slices.Clone(m.Attachments)
	if m.Forward != nil {
		f := *m.Forward
		f.Attachments = slices.Clone(f.Attachments)
		m.Forward = &f
	}
	return m
}

// scanMessageLines is the single decoder for indexed and arbitrary-offset reads.
func (s *Store) scanMessageLines(roomID string, input io.Reader, offset int64, visit func(messageLine)) (int64, error) {
	projections, err := s.postNormalizations(roomID, offset)
	if err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(input, 64*1024)
	refs := issueReferences{store: s}
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			if err != nil && err != io.EOF {
				return offset, err
			}
			return offset, nil
		}
		entry := messageLine{start: offset, end: offset + int64(len(line))}
		offset = entry.end
		canonical := line[:len(line)-1]
		omitted := false
		if projection, ok := projections[entry.start]; ok {
			digest := sha256.Sum256(line)
			if projection.end != entry.end || !bytes.Equal(projection.digest, digest[:]) {
				return entry.start, fmt.Errorf("room %s historical post source changed at offset %d", roomID, entry.start)
			}
			canonical, omitted = projection.body, projection.omitted
		}
		if !omitted && len(line) <= maxRecordBytes {
			decode := s.decodeMessage
			if decode == nil {
				decode = func(b []byte, m *Message) error { return json.Unmarshal(b, m) }
			}
			if decode(canonical, &entry.msg) != nil {
				entry.msg = Message{}
			}
			// A label is cosmetic: a failed lookup keeps the stored message ID
			// instead of failing the room read.
			_ = refs.canonical(roomID, entry.msg.References)
			if f := entry.msg.Forward; f != nil {
				_ = refs.canonical(f.RoomID, f.References)
			}
		}
		visit(entry)
	}
}

// Watch lists rooms on each pass, so it also releases indexes for rooms that
// another process deleted. This keeps their retained files and history bounded.
func (s *Store) pruneMessageLogs(records []Record) {
	paths := make(map[string]bool, len(records))
	for _, rec := range records {
		path, _ := s.messagesPath(rec.ID)
		paths[path] = true
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	for path := range s.logs {
		if !paths[path] {
			s.dropMessageLog(path)
		}
	}
}

// Follow visits every post of a room in order, then each new post as it is
// stored, until visit returns true, visit fails or ctx ends.
func (s *Store) Follow(ctx context.Context, roomID string, visit func(Message) (bool, error)) error {
	offset := int64(0)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		msgs, next, err := s.ReadFrom(roomID, offset)
		if err != nil {
			return err
		}
		offset = next
		for _, msg := range msgs {
			if stop, err := visit(msg); stop || err != nil {
				return err
			}
		}
		if len(msgs) > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
