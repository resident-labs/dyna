package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/box"
	"net/url"
)

// DeployHosts is a read-only snapshot for an independent staging process. It never opens a Store, takes its engine lock,
// migrates a database, or consults new schema columns while the old engine runs.
func DeployHosts(ctx context.Context, data string) ([]box.Host, error) {
	db, err := sql.Open("sqlite", "file:"+url.PathEscape((box.Layout{Data: data}).Store())+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return queryAll(ctx, db, func(rows scanner) (h box.Host, err error) {
		err = rows.Scan(&h.Name, &h.SSH)
		return
	}, `SELECT name,ssh FROM hosts WHERE state='ready' AND exec_only=0 ORDER BY name`)
}
