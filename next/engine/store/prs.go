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
		result, err := tx.ExecContext(ctx, "WITH RECURSIVE ancestors(id,parent,closed_at,depth) AS (SELECT id,parent,closed_at,0 FROM sessions WHERE id=? UNION ALL SELECT s.id,s.parent,s.closed_at,a.depth+1 FROM sessions s JOIN ancestors a ON s.id=a.parent) INSERT INTO pr_owners(repo,number,session,goal,approved_head) SELECT ?,?,id,'','' FROM ancestors WHERE closed_at IS NULL ORDER BY depth LIMIT 1", session, repo, number)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return sql.ErrNoRows
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		var recipient contract.SessionID
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent,closed_at,depth) AS (
			SELECT p.id,p.parent,p.closed_at,1 FROM sessions s JOIN sessions p ON p.id=s.parent WHERE s.id=?
			UNION ALL SELECT p.id,p.parent,p.closed_at,a.depth+1 FROM sessions p JOIN ancestors a ON p.id=a.parent
		) SELECT id FROM ancestors WHERE closed_at IS NULL ORDER BY depth LIMIT 1`, session).Scan(&recipient)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if recipient != "" {
			deliveries = append(deliveries, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: recipient}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
		}
		for _, delivery := range deliveries {
			if err := s.InsertDelivery(ctx, tx, seq, delivery); err != nil {
				return err
			}
		}
		return nil
	})
}
