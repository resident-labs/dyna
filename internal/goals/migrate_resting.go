package goals

import (
	"context"
	"database/sql"
)

// migrateResting keeps indexed state and raw goal bodies consistent.
func migrateResting(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	const affected = "state='resting' OR json_extract(body,'$.state')='resting'"
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM goals WHERE "+affected).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='goals.revision'"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "UPDATE goal_rooms SET revision=(SELECT CAST(value AS INTEGER) FROM meta WHERE key='goals.revision') WHERE room IN (SELECT room FROM goals WHERE "+affected+")"); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "UPDATE goals SET state='active',body=json_set(body,'$.state','active') WHERE "+affected)
	return err
}
