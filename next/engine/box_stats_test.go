package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var statsOwner = contract.Principal{Kind: contract.PrincipalOwner}

// A call reaches the box through its channel, and the box's answer comes back to that call with the box's size.
func TestBoxStatsAsksTheBox(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	agent := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	messages, problem := s.BoxChannel(ctx, agent)
	if problem != nil {
		t.Fatal(problem)
	}
	reading := contract.BoxReading{MemUsed: 1 << 30, CPUUsed: 0.5, DiskUsed: 2 << 30}
	go func() {
		for message := range messages {
			if ask, ok := message.Body.(contract.BoxStatsAsk); ok {
				if _, problem := s.BoxStatsReport(ctx, agent, contract.BoxStatsReportRequest{Ask: ask.Ask, Reading: reading}); problem != nil {
					t.Error(problem)
				}
				return
			}
		}
	}()
	got, problem := s.BoxStats(ctx, statsOwner, contract.BoxStatsRequest{Session: id})
	if problem != nil {
		t.Fatal(problem)
	}
	if got.Reading == nil || *got.Reading != reading {
		t.Fatalf("reading %+v", got.Reading)
	}
	if got.Size.CPUs == 0 || got.Size.MemBytes == 0 {
		t.Fatalf("size %+v", got.Size)
	}
}

// A box that does not answer, such as one on an older agent, gives the size and no reading.
func TestBoxStatsGivesUpOnASilentBox(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	attachTestChannel(t, s, id)
	waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	got, problem := s.BoxStats(waiting, statsOwner, contract.BoxStatsRequest{Session: id})
	if problem != nil {
		t.Fatal(problem)
	}
	if got.Reading != nil || got.Size.CPUs == 0 {
		t.Fatalf("got %+v", got)
	}
}

// A box that is not running is not asked, so the answer comes at once.
func TestBoxStatsOfAStoppedBoxHasNoReading(t *testing.T) {
	s, room, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	channel := attachTestChannel(t, s, id)
	record, err := s.store.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, room, current, contract.BoxStopped, "", ""); err != nil {
		t.Fatal(err)
	}
	got, problem := s.BoxStats(ctx, statsOwner, contract.BoxStatsRequest{Session: id})
	if problem != nil {
		t.Fatal(problem)
	}
	if got.Reading != nil || got.Size.CPUs == 0 {
		t.Fatalf("got %+v", got)
	}
	select {
	case ask := <-channel.Asks():
		t.Fatalf("a stopped box was asked: %d", ask)
	default:
	}
}

func TestBoxStatsIsTheOwnersAndItsReportTheBoxes(t *testing.T) {
	s, _, id, ctx := setup(t)
	agent := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	if _, problem := s.BoxStats(ctx, agent, contract.BoxStatsRequest{Session: id}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session read box stats: %v", problem)
	}
	if _, problem := s.BoxStatsReport(ctx, statsOwner, contract.BoxStatsReportRequest{}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("the owner reported box stats: %v", problem)
	}
}
