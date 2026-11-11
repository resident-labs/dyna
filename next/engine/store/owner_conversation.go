package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

const ownerConversationQuery = `SELECT d.reply_home FROM turns t CROSS JOIN deliveries d ON d.turn=t.id
	WHERE d.to_kind=? AND d.to_id=? AND t.session=? AND t.ended_at IS NULL AND d.reply_kind=? AND d.reply_target_kind=?
	ORDER BY d.seq DESC LIMIT 1`

// OwnerConversation is where a session's post to the owner goes when it names no home: the home of the final reply its
// open turn owes the owner, else wherever the owner last wrote to it, else the feed. It reads deliveries and stores nothing.
func (s *Store) OwnerConversation(ctx context.Context, session contract.SessionID) (contract.Conversation, error) {
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: session}
	var home string
	err := s.Read().QueryRowContext(ctx, ownerConversationQuery, contract.PrincipalSession, session, session, contract.ReplyFinal, contract.PrincipalOwner).Scan(&home)
	if err == nil {
		kind, id, _ := strings.Cut(home, ":")
		switch {
		case contract.ConversationKind(kind) == contract.ConversationFeed:
			return feed, nil
		case contract.ConversationKind(kind) == contract.ConversationDM && contract.SessionID(id) == session:
			return dm, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return feed, err
	}
	// Engine notices and service events carry no words of the owner's, so they say nothing about where the owner is.
	var surface contract.Surface
	err = s.Read().QueryRowContext(ctx, `SELECT d.surface,d.reply_home FROM deliveries d JOIN events e ON e.seq=d.seq
		WHERE d.to_kind=? AND d.to_id=? AND e.from_kind=? AND d.surface NOT IN (?,?)
		ORDER BY d.seq DESC LIMIT 1`, contract.PrincipalSession, session, contract.PrincipalOwner, contract.SurfaceEngine, contract.SurfaceService).Scan(&surface, &home)
	if errors.Is(err, sql.ErrNoRows) {
		return feed, nil
	}
	if err != nil {
		return feed, err
	}
	if surface == contract.SurfaceDM || home == string(contract.ConversationDM)+":"+string(session) {
		return dm, nil
	}
	return feed, nil
}
