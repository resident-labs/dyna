package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// PostReceipts derives display state from deliveries; no receipt state is persisted.
// recipientRoom restricts recipients to an authorized room; empty allows all rooms
// for an owner. Room identity comes from durable sessions, not live boxes. Missing
// identities are omitted because their room cannot be established. Apply this
// filter before reading names, states or batch deadlines for hidden recipients.
// Close discards unread deliveries. Only explicit mentions durably identify those
// recipients afterward; implicit recipients without a row cannot be reconstructed.
func (s *Store) PostReceipts(ctx context.Context, seq int64, recipientRoom contract.RoomID, now time.Time) ([]contract.PostReceipt, error) {
	rows, err := s.Read().QueryContext(ctx, `WITH recipients AS (
 SELECT d.to_id,d.state,d.wake FROM deliveries d WHERE d.seq=?1 AND d.to_kind='session'
 UNION
 SELECT json_extract(m.value,'$.session'),'closed',0
 FROM events e, json_each(CAST(e.body AS TEXT),'$.mentions') m
 JOIN sessions s ON s.id=json_extract(m.value,'$.session')
 WHERE e.seq=?1 AND e.type='post.created' AND json_extract(m.value,'$.kind')='session'
 AND s.closed_at IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.seq=?1 AND d.to_kind='session' AND d.to_id=s.id)
 )
 SELECT d.to_id,s.name,d.state,s.closed_at IS NOT NULL,
 COALESCE(d.wake=1 AND s.state='open' AND b.state='ready' AND b.want='run',0)
 FROM recipients d JOIN sessions s ON s.id=d.to_id LEFT JOIN boxes b ON b.session=s.id
 WHERE (?2='' OR s.room=?2) ORDER BY d.to_id`, seq, recipientRoom)
	if err != nil {
		return nil, err
	}
	var out []contract.PostReceipt
	var eligible []bool
	for rows.Next() {
		var r contract.PostReceipt
		var active bool
		if err := rows.Scan(&r.Session, &r.Name, &r.State, &r.Closed, &active); err != nil {
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
