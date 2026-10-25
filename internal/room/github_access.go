package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/repoid"
)

// GitHubAccess is the room's explicit permission to write to GitHub.
type GitHubAccess string

const (
	GitHubReadOnly  GitHubAccess = "read-only"
	GitHubReadWrite GitHubAccess = "read-write"
)

func ValidateGitHubAccess(value GitHubAccess) error {
	if value != GitHubReadOnly && value != GitHubReadWrite {
		return fmt.Errorf("GitHub access must be read-only or read-write")
	}
	return nil
}

// backfillGitHubAccess saves each old room's effective policy once at startup.
func backfillGitHubAccess(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT body FROM room_records WHERE COALESCE(json_extract(body, '$.githubAccess'), '') = ''`)
	if err != nil {
		return err
	}
	var records []Record
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		var rec Record
		if err := json.Unmarshal([]byte(body), &rec); err != nil {
			rows.Close()
			return err
		}
		records = append(records, rec)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.GitHubAccess = legacyGitHubAccess(ctx, rec)
		if err := putRecord(ctx, conn, rec); err != nil {
			return err
		}
	}
	return nil
}

// TODO: legacy/shim remove later — preserve the former organization default
// only for the one-time room.github_access migration. Never use for new rooms.
func legacyGitHubAccess(ctx context.Context, rec Record) GitHubAccess {
	if rec.Repo != "" {
		remote, err := repoid.Git(ctx, rec.Repo, "remote", "get-url", "origin")
		if err == nil {
			identity, err := repoid.RemoteIdentity(rec.Repo, remote)
			if err == nil && strings.HasPrefix(strings.ToLower(identity), "github.com/resident-labs/") {
				return GitHubReadWrite
			}
		}
	}
	return GitHubReadOnly
}
