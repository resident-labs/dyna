package goals

import (
	"context"
	"database/sql"
	"time"
)

// migrateServiceGoals drops open goals that an old build marked service:true,
// then removes the retired key from every goal body.
func (s *Store) migrateServiceGoals(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	const marked = "json_extract(body,'$.service')=1"
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM goals WHERE json_type(body,'$.service') IS NOT NULL").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='goals.revision'"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "UPDATE goal_rooms SET revision=(SELECT CAST(value AS INTEGER) FROM meta WHERE key='goals.revision') WHERE room IN (SELECT room FROM goals WHERE json_type(body,'$.service') IS NOT NULL)"); err != nil {
		return err
	}
	at := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := conn.ExecContext(ctx, `UPDATE goals SET state='dropped',body=json_set(body,'$.state','dropped','$.updatedAt',?1,'$.done',json_object('at',?1,'by','owner','summary','Root and service sessions no longer hold goals.')) WHERE state='active' AND `+marked, at); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "UPDATE goals SET body=json_remove(body,'$.service') WHERE json_type(body,'$.service') IS NOT NULL")
	return err
}
