package engine

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// boxStatsWait bounds a box.stats call, so a box that stopped answering costs the panel one slow read and no more.
const boxStatsWait = 2 * time.Second

// BoxStats asks a running box what it is using. The engine holds no reading: each one is the box's answer to this call.
func (s *Service) BoxStats(ctx context.Context, caller contract.Principal, req contract.BoxStatsRequest) (contract.BoxStatsResponse, *contract.Error) {
	var response contract.BoxStatsResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	session, problem := s.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	box, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return response, failure(err)
	}
	response.Size = box.Size
	handle, ok := s.live.LookupSession(session.ID)
	if box.State != contract.BoxReady || !ok || !handle.Lock() {
		return response, nil
	}
	channel := handle.Channel()
	if channel == nil {
		handle.Unlock()
		return response, nil
	}
	ask, answer := channel.AskStats()
	handle.Unlock()
	defer func() {
		if handle.Lock() {
			channel.ForgetStats(ask)
			handle.Unlock()
		}
	}()
	timer := time.NewTimer(boxStatsWait)
	defer timer.Stop()
	select {
	case reading := <-answer:
		response.Reading = &reading
	case <-timer.C:
	case <-ctx.Done():
	}
	return response, nil
}

// BoxStatsReport hands a box's answer to the call waiting for it. A late answer has no one to reach, and is dropped.
func (s *Service) BoxStatsReport(_ context.Context, caller contract.Principal, req contract.BoxStatsReportRequest) (contract.BoxStatsReportResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.BoxStatsReportResponse{}, forbidden()
	}
	handle, ok := s.live.LookupSession(caller.Session)
	if !ok || !handle.Lock() {
		return contract.BoxStatsReportResponse{}, nil
	}
	defer handle.Unlock()
	if channel := handle.Channel(); channel != nil {
		channel.AnswerStats(req.Ask, req.Reading)
	}
	return contract.BoxStatsReportResponse{}, nil
}
