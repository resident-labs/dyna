package engine

import (
	"context"
	"log"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	// idleCompactTokens is the context size over which a quiet box compacts before it stops. A stopped box wakes to a cold
	// prompt cache, so its next turn pays for the whole context. It is the size model transfer compacts at, for the same reason.
	idleCompactTokens = 100_000
	// idleCompactWait bounds how long a stop waits for an answer, past the box agent's own limit. An older box agent skips the
	// message and never answers.
	idleCompactWait = 11 * time.Minute
)

// CompactBeforeStop asks a box about to stop for being quiet to compact, the first time this quiet period, and reports true
// while the stop should wait for the answer. Nothing here may block the stop for long: an unanswered ask runs out, and a
// failed one is recorded so that it is not tried again.
func (s *Service) CompactBeforeStop(ctx context.Context, id contract.SessionID) bool {
	turn, due, err := s.store.CompactionDue(ctx, id)
	if err != nil {
		log.Printf("session %s: compaction before stop: %v", id, err)
		return false
	}
	handle, ok := s.live.LookupSession(id)
	if !due || !ok || !handle.Lock() {
		return false
	}
	defer handle.Unlock()
	channel := handle.Channel()
	return channel != nil && channel.AskCompact(turn, s.now().Add(idleCompactWait), s.now())
}

// BoxCompacted takes the box's answer. Pi wrote any compaction to the transcript already, so this only records it.
func (s *Service) BoxCompacted(ctx context.Context, caller contract.Principal, req contract.BoxCompactedRequest) (contract.BoxCompactedResponse, *contract.Error) {
	var response contract.BoxCompactedResponse
	if caller.Kind != contract.PrincipalSession {
		return response, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return response, failure(err)
	}
	if req.Compacted || req.Error != "" {
		models, err := s.store.RecentTurnModels(ctx, session.ID)
		if err != nil {
			return response, failure(err)
		}
		if len(models) > 0 {
			req.Usage.Model = models[0]
		}
		event, err := s.event(session.Room, caller, contract.SessionCompacted{Session: session.ID, Turn: req.Turn, TokensBefore: req.TokensBefore, TokensAfter: req.TokensAfter, Usage: req.Usage, Error: excerpt(req.Error, 500)})
		if err != nil {
			return response, failure(err)
		}
		if err := s.store.RecordEvent(ctx, event); err != nil {
			return response, failure(err)
		}
	}
	if handle, ok := s.live.LookupSession(session.ID); ok && handle.Lock() {
		if channel := handle.Channel(); channel != nil {
			channel.CompactAnswered()
		}
		handle.Unlock()
	}
	s.reconciler.Kick()
	return response, nil
}
