package goals

import (
	"context"
	"database/sql"
)

// migrateOwnerHeldGoals marks the open goals that an old build left with the
// owner. Root sees them as work only it can move: it asks the owner about each
// one, then drops or finishes it, or hands it to an open session.
// TODO: legacy/shim remove later — no build leaves a goal with the owner now; remove this migration and the mark after every stored goal is resolved.
func migrateOwnerHeldGoals(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	const ownerHeld = "state='active' AND json_extract(body,'$.setter')='owner' AND COALESCE(json_extract(body,'$.shared'),0)=0 AND COALESCE(json_array_length(json_extract(body,'$.holders')),0)=0"
	const marked = ownerHeld + " AND json_type(body,'$.legacyOwnerHeld') IS NULL"
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM goals WHERE "+marked).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='goals.revision'"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "UPDATE goal_rooms SET revision=(SELECT CAST(value AS INTEGER) FROM meta WHERE key='goals.revision') WHERE room IN (SELECT room FROM goals WHERE "+marked+")"); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "UPDATE goals SET body=json_set(body,'$.legacyOwnerHeld',json('true')) WHERE "+marked)
	return err
}
