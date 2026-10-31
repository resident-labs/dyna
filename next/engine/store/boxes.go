package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (s *Store) Boxes(ctx context.Context) ([]box.Box, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT id,session,parent,host,image,size,state,reason,slice,created_at,updated_at FROM boxes ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boxes []box.Box
	for rows.Next() {
		var b box.Box
		var parent, host sql.NullString
		var size contract.BoxSizeName
		var created, updated int64
		if err := rows.Scan(&b.ID, &b.Session, &parent, &host, &b.Image, &size, &b.State, &b.Reason, &b.Slice, &created, &updated); err != nil {
			return nil, err
		}
		b.BudgetParent = contract.BoxID(parent.String)
		b.Host = contract.HostName(host.String)
		var ok bool
		b.Size, ok = box.Size(size)
		if !ok {
			return nil, box.TransitionError{From: b.State, To: b.State}
		}
		b.PlacedAt = time.UnixMilli(created)
		b.Updated = time.UnixMilli(updated)
		boxes = append(boxes, b)
	}
	return boxes, rows.Err()
}

func (s *Store) Box(ctx context.Context, id contract.BoxID) (box.Box, error) {
	boxes, err := s.Boxes(ctx)
	if err != nil {
		return box.Box{}, err
	}
	for _, b := range boxes {
		if b.ID == id {
			return b, nil
		}
	}
	return box.Box{}, sql.ErrNoRows
}

// TransitionBox changes the record and publishes its event in one transaction.
func (s *Store) TransitionBox(ctx context.Context, b box.Box, to contract.BoxState, reason string, event contract.Event) (box.Box, error) {
	next, body, err := box.Transition(b, to, reason, event.Time)
	if err != nil {
		return box.Box{}, err
	}
	body.Cause = event.Body.(contract.BoxChanged).Cause
	event.Body = body
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var host *contract.HostName
		if next.Host != "" {
			host = &next.Host
		}
		result, err := tx.ExecContext(ctx, "UPDATE boxes SET host=?,image=?,size=?,state=?,reason=?,slice=?,updated_at=? WHERE id=? AND state=?", host, next.Image, next.Size.Name, next.State, next.Reason, next.Slice, event.Time.UnixMilli(), next.ID, b.State)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return next, err
}

// BoxAncestry returns budget ancestors and the box, parent first.
func (s *Store) BoxAncestry(ctx context.Context, id contract.BoxID) ([]box.Box, error) {
	rows, err := s.Read().QueryContext(ctx, `WITH RECURSIVE path(id,session,parent,state,depth) AS (
  SELECT id,session,parent,state,0 FROM boxes WHERE id=?
  UNION ALL SELECT b.id,b.session,b.parent,b.state,p.depth+1 FROM boxes b JOIN path p ON b.id=p.parent
 ) SELECT id,session,state FROM path ORDER BY depth DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []box.Box
	for rows.Next() {
		var b box.Box
		if err := rows.Scan(&b.ID, &b.Session, &b.State); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
