package state

import (
	"context"
	"database/sql"
)

// migrateLanding moves each retired landing object into reviewHistory, so its
// evidence stays on the record. Null landing values are only removed.
func migrateLanding(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM pr_records WHERE json_type(body,'$.record.landing') IS NOT NULL").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	var revision int64
	if err := conn.QueryRowContext(ctx, "SELECT revision+1 FROM pr_state WHERE id=1").Scan(&revision); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE pr_records SET rev=?1,body=json_remove(json_set(body,'$.record.reviewHistory',json_insert(coalesce(json_extract(body,'$.record.reviewHistory'),'[]'),'$[#]',json_extract(body,'$.record.landing'))),'$.record.landing') WHERE json_type(body,'$.record.landing')='object'`, revision); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "UPDATE pr_records SET rev=?1,body=json_remove(body,'$.record.landing') WHERE json_type(body,'$.record.landing')='null'", revision); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "UPDATE pr_state SET revision=? WHERE id=1", revision)
	return err
}
