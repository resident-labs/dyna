package engine

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// nextTurn reads the session's next TurnStart.
func nextTurn(t *testing.T, s *Service, ctx context.Context, session contract.SessionID) contract.TurnStart {
	t.Helper()
	channelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	channel, problem := testChannel(t, s, channelCtx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if problem != nil {
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range channel {
		}
	}()
	for message := range channel {
		if start, ok := message.Body.(contract.TurnStart); ok {
			return start
		}
	}
	t.Fatal("the channel ended without a turn")
	return contract.TurnStart{}
}

// wake gives an idle session a reason to start a turn.
func wake(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, session contract.SessionID) {
	t.Helper()
	home := contract.Conversation{Kind: contract.ConversationDM, Session: session}
	if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: home, Text: "next"}); problem != nil {
		t.Fatal(problem)
	}
}
