package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
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
	rows, err := db.QueryContext(ctx, `SELECT name,ssh FROM hosts WHERE state='ready' AND exec_only=0 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []box.Host
	for rows.Next() {
		var name contract.HostName
		var ssh string
		if err := rows.Scan(&name, &ssh); err != nil {
			return nil, err
		}
		hosts = append(hosts, box.Host{Name: name, SSH: ssh})
	}
	return hosts, rows.Err()
}
