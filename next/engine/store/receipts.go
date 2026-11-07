package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// PostReceipts derives display state from deliveries; no receipt state is persisted.
func (s *Store) PostReceipts(ctx context.Context, seq int64, now time.Time) ([]contract.PostReceipt, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT d.to_id,s.name,d.state,
 COALESCE(d.wake=1 AND s.state='open' AND b.state='ready' AND b.want='run',0)
 FROM deliveries d JOIN sessions s ON s.id=d.to_id LEFT JOIN boxes b ON b.session=s.id
 WHERE d.seq=? AND d.to_kind='session' ORDER BY d.to_id`, seq)
	if err != nil {
		return nil, err
	}
	var out []contract.PostReceipt
	var eligible []bool
	for rows.Next() {
		var r contract.PostReceipt
		var active bool
		if err := rows.Scan(&r.Session, &r.Name, &r.State, &active); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
		eligible = append(eligible, active)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].State != "pending" || !eligible[i] {
			continue
		}
		deadline, err := s.InputBatchDeadline(ctx, out[i].Session)
		if err != nil {
			return nil, err
		}
		if deadline.After(now) {
			out[i].BatchDeadline = &deadline
		}
	}
	return out, nil
}
