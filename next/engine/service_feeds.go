package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// ensureFeedFailureSubscription ensures a dead feed service reaches someone in the room: if no subscription
// already watches service.failed for this service and the room has an open root session, it adds one waking root.
// Otherwise it returns a hint describing what to add.
func (s *Service) ensureFeedFailureSubscription(ctx context.Context, room contract.RoomID, name contract.ServiceName) string {
	subs, err := s.store.RoomSubscriptions(ctx, room)
	if err != nil {
		return ""
	}
	probe := contract.ServiceFailure{Name: name}
	for _, sub := range subs {
		if !slices.Contains(sub.On, probe.EventType()) {
			continue
		}
		if contract.MatchesWhere(sub.Where, probe) {
			return ""
		}
	}
	root, err := s.store.RoomRootSession(ctx, room)
	if err != nil {
		return ""
	}
	if root == "" {
		return fmt.Sprintf("add a subscription so a dead feed reaches someone: dyna subscription add --room %s --on service.failed --where service=%s --wake root", room, name)
	}
	now := s.now()
	subID, err := id("sub_", now)
	if err != nil {
		return ""
	}
	sub := contract.Subscription{
		ID:    contract.SubscriptionID(subID),
		Scope: contract.ScopeRoom,
		Room:  room,
		On:    []contract.EventType{probe.EventType()},
		Where: []string{"service=" + string(name)},
		Wake:  root,
	}
	if err := s.store.AddSubscription(ctx, sub, now); err != nil {
		return fmt.Sprintf("add a subscription so a dead feed reaches someone: dyna subscription add --room %s --on service.failed --where service=%s --wake root", room, name)
	}
	s.pokeSubscriptions()
	return fmt.Sprintf("added subscription %s (on service.failed where service=%s -> wake root)", sub.ID, name)
}

// allowFeedCall enforces the per-service sliding-window rate limit (60 calls/min).
func (s *Service) allowFeedCall(serviceID contract.ServiceID, now time.Time) bool {
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	if s.feedRates == nil {
		s.feedRates = make(map[contract.ServiceID][]time.Time)
	}
	cutoff := now.Add(-time.Minute)
	times := s.feedRates[serviceID]
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= contract.ServiceFeedRatePerMin {
		s.feedRates[serviceID] = kept
		return false
	}
	s.feedRates[serviceID] = append(kept, now)
	return true
}

func (s *Service) feedOutboxChan(serviceID contract.ServiceID) chan struct{} {
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	if s.feedOutboxWait == nil {
		s.feedOutboxWait = make(map[contract.ServiceID]chan struct{})
	}
	ch, ok := s.feedOutboxWait[serviceID]
	if !ok {
		ch = make(chan struct{})
		s.feedOutboxWait[serviceID] = ch
	}
	return ch
}

func (s *Service) wakeFeedOutbox(serviceID contract.ServiceID) {
	s.feedMu.Lock()
	ch, ok := s.feedOutboxWait[serviceID]
	if ok {
		delete(s.feedOutboxWait, serviceID)
	}
	s.feedMu.Unlock()
	if ok {
		close(ch)
	}
}

// ServiceFeedMessage ingests one message posted by a feed service on its per-service Unix socket.
// Room and service name come strictly from the socket listener's binding, never the request body.
func (s *Service) ServiceFeedMessage(ctx context.Context, serviceID contract.ServiceID, room contract.RoomID, name contract.ServiceName, req contract.FeedMessageRequest) (contract.FeedMessageResponse, *contract.Error) {
	now := s.now()
	if !s.allowFeedCall(serviceID, now) {
		return contract.FeedMessageResponse{}, &contract.Error{Code: contract.ErrRateLimited, Message: "feed rate limit exceeded (60 messages per minute)"}
	}
	msgID := strings.TrimSpace(req.ID)
	if msgID == "" || len(msgID) > 256 || strings.IndexFunc(msgID, isControlRune) >= 0 {
		return contract.FeedMessageResponse{}, invalid("id is required (1 to 256 characters, no control characters)")
	}
	author := strings.TrimSpace(req.Author)
	if author == "" || len(author) > 256 || strings.IndexFunc(author, isControlRune) >= 0 {
		return contract.FeedMessageResponse{}, invalid("author is required (1 to 256 characters, no control characters)")
	}
	if len(req.Text) > contract.ServiceFeedTextMax {
		return contract.FeedMessageResponse{}, &contract.Error{Code: contract.ErrTooLarge, Message: fmt.Sprintf("text exceeds %d bytes", contract.ServiceFeedTextMax)}
	}
	var data json.RawMessage
	if len(req.Data) > 0 && string(req.Data) != "null" {
		if len(req.Data) > contract.ServiceFeedDataMax {
			return contract.FeedMessageResponse{}, &contract.Error{Code: contract.ErrTooLarge, Message: fmt.Sprintf("data exceeds %d bytes", contract.ServiceFeedDataMax)}
		}
		data = req.Data
	}
	if req.Text == "" && len(data) == 0 {
		return contract.FeedMessageResponse{}, invalid("text or data is required")
	}
	dup, err := s.store.RecordServiceFeedMessage(ctx, serviceID, room, name, msgID, author, req.Text, data, now, func(r contract.RoomID, body contract.EventBody) (contract.Event, error) {
		return s.event(r, engineSender, body)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return contract.FeedMessageResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "feed service not found"}
		}
		return contract.FeedMessageResponse{}, failure(err)
	}
	return contract.FeedMessageResponse{OK: true, Duplicate: dup}, nil
}

// ServiceFeedOutbox returns unacknowledged outbound messages for serviceID, long-polling up to wait when empty.
func (s *Service) ServiceFeedOutbox(ctx context.Context, serviceID contract.ServiceID, wait time.Duration, limit int) (contract.FeedOutboxResponse, *contract.Error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if wait < 0 {
		wait = 0
	}
	if wait > time.Minute {
		wait = time.Minute
	}
	var deadline <-chan time.Time
	var timer *time.Timer
	for {
		// Subscribe before reading when waiting, so a concurrent ServiceSend cannot slip between read and wait.
		var notify chan struct{}
		if wait > 0 {
			notify = s.feedOutboxChan(serviceID)
		}
		msgs, err := s.store.PendingServiceOutbox(ctx, serviceID, limit)
		if err != nil {
			if timer != nil {
				timer.Stop()
			}
			return contract.FeedOutboxResponse{}, failure(err)
		}
		if len(msgs) > 0 || wait == 0 {
			if timer != nil {
				timer.Stop()
			}
			return contract.FeedOutboxResponse{Messages: msgs}, nil
		}
		if timer == nil {
			timer = time.NewTimer(wait)
			deadline = timer.C
		}
		select {
		case <-notify:
		case <-deadline:
			timer.Stop()
			return contract.FeedOutboxResponse{Messages: []contract.FeedOutboxMessage{}}, nil
		case <-ctx.Done():
			timer.Stop()
			return contract.FeedOutboxResponse{}, failure(ctx.Err())
		}
	}
}

// ServiceFeedAck acknowledges one outbound message belonging to serviceID.
func (s *Service) ServiceFeedAck(ctx context.Context, serviceID contract.ServiceID, msgID string) *contract.Error {
	if msgID == "" || len(msgID) > 128 || strings.ContainsAny(msgID, "/\\") {
		return invalid("invalid outbox message id")
	}
	err := s.store.AckServiceOutbox(ctx, serviceID, msgID, s.now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &contract.Error{Code: contract.ErrNotFound, Message: "outbox message not found"}
		}
		return failure(err)
	}
	return nil
}

// ServiceSend queues an outbound message for a feed service and records a service.sent audit event.
// Only the owner or sessions listed in the service's senders (default root only) may send.
func (s *Service) ServiceSend(ctx context.Context, who contract.Principal, req contract.ServiceSendRequest) (contract.ServiceSendResponse, *contract.Error) {
	if req.Service == "" {
		return contract.ServiceSendResponse{}, invalid("service is required")
	}
	to := strings.TrimSpace(req.To)
	if to == "" || len(to) > 512 || strings.IndexFunc(to, isControlRune) >= 0 {
		return contract.ServiceSendResponse{}, invalid("to is required (1 to 512 characters, no control characters)")
	}
	if req.Text == "" {
		return contract.ServiceSendResponse{}, invalid("text is required")
	}
	if len(req.Text) > contract.ServiceFeedOutboxMax {
		return contract.ServiceSendResponse{}, &contract.Error{Code: contract.ErrTooLarge, Message: fmt.Sprintf("text exceeds %d bytes", contract.ServiceFeedOutboxMax)}
	}
	if len(req.ToolCallID) > 4096 || (req.ToolCallID != "" && who.Kind != contract.PrincipalSession) {
		return contract.ServiceSendResponse{}, invalid("invalid tool call ID")
	}
	room := req.Room
	var callerSession store.Session
	if who.Kind == contract.PrincipalSession {
		var problem *contract.Error
		callerSession, problem = s.callerSession(ctx, who)
		if problem != nil {
			return contract.ServiceSendResponse{}, problem
		}
		if room != "" && room != callerSession.Room {
			return contract.ServiceSendResponse{}, contract.ForeignRoom(room, callerSession.Room)
		}
		room = callerSession.Room
	} else if who.Kind != contract.PrincipalOwner {
		return contract.ServiceSendResponse{}, forbidden()
	}
	if room == "" {
		return contract.ServiceSendResponse{}, invalid("room is required")
	}
	row, problem := s.serviceNamed(ctx, contract.ServiceRef{Room: room, Name: req.Service})
	if problem != nil {
		return contract.ServiceSendResponse{}, problem
	}
	if !row.Spec.Feed {
		return contract.ServiceSendResponse{}, invalid(fmt.Sprintf("service %s is not a feed service", req.Service))
	}
	if who.Kind == contract.PrincipalSession && !sessionCanSend(callerSession, row.Spec.Senders) {
		if len(row.Spec.Senders) == 0 {
			return contract.ServiceSendResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: fmt.Sprintf("only root may send to feed service %s", req.Service)}
		}
		return contract.ServiceSendResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: fmt.Sprintf("session %s is not allowed to send to feed service %s", callerSession.Name, req.Service)}
	}
	now := s.now()
	msgID, err := id("out_", now)
	if err != nil {
		return contract.ServiceSendResponse{}, failure(err)
	}
	err = s.store.EnqueueServiceOutbox(ctx, row.ID, room, row.Spec.Name, msgID, to, req.Text, req.ToolCallID, now, func(r contract.RoomID, body contract.EventBody) (contract.Event, error) {
		return s.event(r, who, body)
	})
	if err != nil {
		return contract.ServiceSendResponse{}, serviceFailure(err)
	}
	s.wakeFeedOutbox(row.ID)
	return contract.ServiceSendResponse{ID: msgID}, nil
}

func sessionCanSend(sess store.Session, senders []string) bool {
	if len(senders) == 0 {
		return sess.Root
	}
	if slices.Contains(senders, sess.Name) || slices.Contains(senders, string(sess.ID)) {
		return true
	}
	return sess.Root && slices.Contains(senders, "root")
}

func isControlRune(r rune) bool { return r < 0x20 || r == 0x7f }
