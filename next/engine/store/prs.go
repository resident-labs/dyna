package store

import (
	"context"
	"database/sql"

	"github.com/resident-labs/dyna/contract"
)

// RecordPROpen makes session the owner of a PR and appends its event and deliveries in one transaction.
// The goal and approved head start empty.
func (s *Store) RecordPROpen(ctx context.Context, repo contract.RepoName, number int, session contract.SessionID, event contract.Event, deliveries []contract.Delivery) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES(?,?,?,'','')", repo, number, session); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if err := insertDelivery(ctx, tx, seq, delivery); err != nil {
				return err
			}
		}
		return nil
	})
}
