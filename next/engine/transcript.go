package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/store"
	"path/filepath"

	"github.com/resident-labs/dyna/contract"
)

// TranscriptAppend stores a box's transcript bytes at the acknowledged offset.
// Bytes below the offset are already stored, so only the part past it is written.
func (s *Service) TranscriptAppend(ctx context.Context, caller contract.Principal, req contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TranscriptAppendResponse{}, forbidden()
	}
	if !filepath.IsLocal(req.Path) || req.Offset < 0 {
		return contract.TranscriptAppendResponse{}, invalid("transcript path must be local and offset must not be negative")
	}
	room, err := s.store.TranscriptRoom(ctx, caller.Session)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	s.transcriptMu.Lock()
	defer s.transcriptMu.Unlock()
	acked, err := s.store.TranscriptOffset(ctx, caller.Session, req.Path)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if req.Offset > acked {
		return contract.TranscriptAppendResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "transcript chunk starts past the acknowledged offset"}
	}
	name := filepath.Join(s.layout.Transcripts(room, caller.Session), req.Path)
	// A copy shorter than its acknowledgment is gone, as for a session open across the move to room folders. Writing at the
	// offset would leave a hole that reads as a transcript, so the answer rewinds the box, which still has the whole file.
	if info, err := os.Stat(name); acked > 0 && (errors.Is(err, os.ErrNotExist) || err == nil && info.Size() < acked) {
		if err := s.store.ResetTranscriptOffset(ctx, caller.Session, req.Path); err != nil {
			return contract.TranscriptAppendResponse{}, failure(err)
		}
		return contract.TranscriptAppendResponse{}, nil
	}
	end := req.Offset + int64(len(req.Bytes))
	if end <= acked {
		return contract.TranscriptAppendResponse{AckedOffset: acked}, nil
	}
	var file *os.File
	err = s.roomWrite(ctx, room, func() error {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		var err error
		if file, err = os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600); err != nil {
			return err
		}
		if _, err := file.WriteAt(req.Bytes[acked-req.Offset:], acked); err != nil {
			return err
		}
		return file.Sync()
	})
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}

	var failed []contract.ToolFailed
	if err := s.indexTranscript(ctx, caller.Session, req.Path, file, acked, end, &failed); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	// The bytes are stored, so a failure here is logged: refusing the append would send them, and the events, again.
	for _, body := range failed {
		event, err := s.event(room, caller, body)
		if err == nil {
			err = s.store.RecordEvent(ctx, event)
		}
		if err != nil {
			log.Printf("transcript %s: record tool.failed: %v", caller.Session, err)
		}
	}
	turns, err := s.store.TranscriptTurnsIntersecting(ctx, caller.Session, req.Path, acked, end)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	for _, turn := range turns {
		event, err := s.event(room, caller, contract.TranscriptChanged{Session: caller.Session, Turn: turn})
		if err != nil {
			return contract.TranscriptAppendResponse{}, failure(err)
		}
		s.live.Publish(room, event, true)
	}

	return contract.TranscriptAppendResponse{AckedOffset: end}, nil
}

// TranscriptFetch reads only bytes committed for the calling session.
func (s *Service) TranscriptFetch(ctx context.Context, caller contract.Principal, req contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TranscriptFetchResponse{}, forbidden()
	}
	if !filepath.IsLocal(req.Path) || req.Offset < 0 {
		return contract.TranscriptFetchResponse{}, invalid("invalid transcript path or offset")
	}
	room, err := s.store.TranscriptRoom(ctx, caller.Session)
	if err != nil {
		return contract.TranscriptFetchResponse{}, failure(err)
	}
	s.transcriptMu.Lock()
	defer s.transcriptMu.Unlock()
	ack, err := s.store.TranscriptOffset(ctx, caller.Session, req.Path)
	if err != nil {
		return contract.TranscriptFetchResponse{}, failure(err)
	}
	if req.Offset > ack {
		return contract.TranscriptFetchResponse{}, invalid("offset past acknowledged transcript")
	}
	f, err := os.Open(filepath.Join(s.layout.Transcripts(room, caller.Session), req.Path))
	if err != nil {
		return contract.TranscriptFetchResponse{}, failure(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return contract.TranscriptFetchResponse{}, failure(err)
	}
	if info.Size() < ack {
		return contract.TranscriptFetchResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "transcript file is shorter than its acknowledged offset"}
	}
	buf := make([]byte, min(ack-req.Offset, 1<<20))
	if len(buf) > 0 {
		if _, err := f.ReadAt(buf, req.Offset); err != nil {
			return contract.TranscriptFetchResponse{}, failure(err)
		}
	}
	return contract.TranscriptFetchResponse{Bytes: buf}, nil
}

// indexTranscript records complete user records and the file acknowledgment in one transaction.
// Each append revisits only the record the last one split.
func (s *Service) indexTranscript(ctx context.Context, session contract.SessionID, path string, file *os.File, acked, end int64, failed *[]contract.ToolFailed) error {
	start := int64(0)
	if acked > 0 {
		const window = 16 << 20
		from := max(0, acked-window)
		buf := make([]byte, acked-from)
		if _, err := file.ReadAt(buf, from); err != nil {
			return err
		}
		if at := bytes.LastIndexByte(buf, '\n'); at >= 0 {
			start = from + int64(at) + 1
		} else {
			start = from
		}
	}
	deliveries, err := s.store.TranscriptDeliveries(ctx, session)
	if err != nil {
		return err
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(file, start, end-start), 64<<10)
	var starts []store.TranscriptTurnStart
	pos := start
	for {
		recordStart := pos
		var line []byte
		oversized := false
		for {
			part, e := reader.ReadSlice('\n')
			pos += int64(len(part))
			if !oversized && len(line)+len(part) <= 16<<20 {
				line = append(line, part...)
			} else {
				oversized = true
				line = nil
			}
			if e == bufio.ErrBufferFull {
				continue
			}
			if e == io.EOF {
				return s.store.CommitTranscriptOffset(ctx, session, path, end, starts)
			}
			if e != nil {
				return e
			}
			break
		}
		if oversized {
			continue
		}
		if bytes.Contains(line, []byte(`"toolCall"`)) || bytes.Contains(line, []byte(`"isError":true`)) {
			s.noteToolCalls(session, line, failed)
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role       string `json:"role"`
				DeliveryID string `json:"deliveryId"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "message" || entry.Message.Role != "user" {
			continue
		}
		id, err := strconv.ParseInt(entry.Message.DeliveryID, 10, 64)
		if err != nil {
			continue
		}
		if turn := deliveries[contract.DeliverySeq(id)]; turn != "" {
			starts = append(starts, store.TranscriptTurnStart{Turn: turn, Start: recordStart})
		}
	}
}

// seenCall is a tool call waiting for its result, kept so a failure can name its arguments and model.
type seenCall struct{ model, args string }

// maxSeenCalls bounds the calls a session keeps waiting; results that succeed are not read, so the map is cleared when full.
const maxSeenCalls = 256

// noteToolCalls remembers an assistant record's tool calls and turns a failed tool result into a tool.failed body. A record
// older than ten minutes is a transcript sent again, as after a rewind, and reports nothing. The caller holds transcriptMu.
func (s *Service) noteToolCalls(session contract.SessionID, line []byte, failed *[]contract.ToolFailed) {
	var entry struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			Role       string `json:"role"`
			Model      string `json:"model"`
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			IsError    bool   `json:"isError"`
			Content    []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "message" {
		return
	}
	message := entry.Message
	calls := s.toolCalls[session]
	switch {
	case message.Role == "assistant":
		for _, part := range message.Content {
			if part.Type != "toolCall" {
				continue
			}
			if calls == nil {
				if s.toolCalls == nil {
					s.toolCalls = map[contract.SessionID]map[string]seenCall{}
				}
				calls = map[string]seenCall{}
				s.toolCalls[session] = calls
			}
			if len(calls) >= maxSeenCalls {
				clear(calls)
			}
			calls[part.ID] = seenCall{model: message.Model, args: excerpt(string(part.Arguments), 600)}
		}
	case message.Role == "toolResult" && message.IsError:
		call := calls[message.ToolCallID]
		delete(calls, message.ToolCallID)
		if s.now().Sub(entry.Timestamp) > 10*time.Minute {
			return
		}
		var text []string
		for _, part := range message.Content {
			if part.Type == "text" {
				text = append(text, part.Text)
			}
		}
		*failed = append(*failed, contract.ToolFailed{Session: session, Tool: message.ToolName, Model: call.model, Args: call.args, Error: excerpt(strings.Join(text, " "), 600)})
	}
}
