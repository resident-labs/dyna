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
