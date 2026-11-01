package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
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
	start := time.Now()
	s.transcriptMu.Lock()
	s.transcriptWait.Add(uint64(time.Since(start)))
	s.transcriptCount.Add(1)
	defer s.transcriptMu.Unlock()
	acked, err := s.store.TranscriptOffset(ctx, caller.Session, req.Path)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	if req.Offset > acked {
		return contract.TranscriptAppendResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "transcript chunk starts past the acknowledged offset"}
	}
	end := req.Offset + int64(len(req.Bytes))
	if end <= acked {
		return contract.TranscriptAppendResponse{AckedOffset: acked}, nil
	}
	name := filepath.Join(s.transcriptDir, string(caller.Session), req.Path)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	defer file.Close()
	tail := req.Bytes[acked-req.Offset:]
	if _, err := file.WriteAt(tail, acked); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	start = time.Now()
	err = file.Sync()
	s.transcriptSync.Add(uint64(time.Since(start)))
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}

	if err := s.indexTranscript(ctx, caller.Session, req.Path, file, acked, end); err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	turns, err := s.store.TranscriptTurnsIntersecting(ctx, caller.Session, req.Path, acked, end)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.TranscriptAppendResponse{}, failure(err)
	}
	for _, turn := range turns {
		event, err := s.event(session.Room, caller, contract.TranscriptChanged{Session: caller.Session, Turn: turn})
		if err != nil {
			return contract.TranscriptAppendResponse{}, failure(err)
		}
		s.live.Publish(session.Room, event, true)
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
	s.transcriptMu.Lock()
	defer s.transcriptMu.Unlock()
	ack, err := s.store.TranscriptOffset(ctx, caller.Session, req.Path)
	if err != nil {
		return contract.TranscriptFetchResponse{}, failure(err)
	}
	if req.Offset > ack {
		return contract.TranscriptFetchResponse{}, invalid("offset past acknowledged transcript")
	}
	f, err := os.Open(filepath.Join(s.transcriptDir, string(caller.Session), req.Path))
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
// An old unindexed file is scanned from its start; later appends revisit only the split record.
func (s *Service) indexTranscript(ctx context.Context, session contract.SessionID, path string, file *os.File, acked, end int64) error {
	indexed, err := s.store.TranscriptPathIndexed(ctx, session, path)
	if err != nil {
		return err
	}
	start := int64(0)
	if indexed && acked > 0 {
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

// TranscriptTiming contains cumulative append timings in nanoseconds.
type TranscriptTiming struct{ WaitNS, SyncNS, Count uint64 }

// TranscriptTiming returns cumulative append lock wait and file sync nanoseconds.
func (s *Service) TranscriptTiming() TranscriptTiming {
	return TranscriptTiming{s.transcriptWait.Load(), s.transcriptSync.Load(), s.transcriptCount.Load()}
}
