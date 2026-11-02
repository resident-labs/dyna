package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// FeedCursor is how far one feed of one repository has been read. At is the newest time a feed item carried, by GitHub's clock,
// and Seen holds the items near it by their GitHub IDs, each with its time, because a feed can show an older item late. Start
// is where the feed began: nothing from then or before is news. Branch and Head belong to the default branch's feed.
type FeedCursor struct {
	At     time.Time        `json:"at,omitzero"`
	Start  time.Time        `json:"start,omitzero"`
	Seen   map[string]int64 `json:"seen,omitempty"`
	Branch string           `json:"branch,omitempty"`
	Head   string           `json:"head,omitempty"`
}

// GitHubFeed is one github_feeds row. A row's ETag only moves with its cursor, in the write that holds what they found.
type GitHubFeed struct {
	Repo   contract.RepoName
	Feed   string
	ETag   string
	Cursor FeedCursor
}

// GitHubFeeds reads every feed's row, by repository and then by feed.
func (s *Store) GitHubFeeds(ctx context.Context) (map[contract.RepoName]map[string]GitHubFeed, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT repo,feed,etag,cursor FROM github_feeds")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	feeds := map[contract.RepoName]map[string]GitHubFeed{}
	for rows.Next() {
		var feed GitHubFeed
		var cursor string
		if err := rows.Scan(&feed.Repo, &feed.Feed, &feed.ETag, &cursor); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cursor), &feed.Cursor); err != nil {
			return nil, err
		}
		if feeds[feed.Repo] == nil {
			feeds[feed.Repo] = map[string]GitHubFeed{}
		}
		feeds[feed.Repo][feed.Feed] = feed
	}
	return feeds, rows.Err()
}

// saveFeeds stores what a pass read. A repository that nothing watches any more drops its feeds, so coming back starts at the head.
func saveFeeds(ctx context.Context, tx *sql.Tx, feeds []GitHubFeed) error {
	for _, feed := range feeds {
		cursor, err := json.Marshal(feed.Cursor)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO github_feeds(repo,feed,etag,cursor) VALUES(?,?,?,?) ON CONFLICT(repo,feed) DO UPDATE SET etag=excluded.etag,cursor=excluded.cursor", feed.Repo, feed.Feed, feed.ETag, string(cursor)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM github_feeds WHERE repo NOT IN (SELECT repo FROM repos UNION SELECT repo FROM pr_owners UNION SELECT repo FROM issue_owners)")
	return err
}

// appendItems adds feed items as quiet events in the repository's default room, and only those a subscription lists: a busy
// repository shows thousands of items a day, and nothing else reads them. An upstream no room lists has no one to tell.
func (s *Store) appendItems(ctx context.Context, tx *sql.Tx, items []contract.GitHubItem, at time.Time) error {
	for _, item := range items {
		body := item.Event()
		if body == nil {
			continue
		}
		var room contract.RoomID
		err := tx.QueryRowContext(ctx, "SELECT r.default_room FROM repos r WHERE r.repo=? AND EXISTS(SELECT 1 FROM subscription_events e JOIN subscriptions s ON s.id=e.sub WHERE s.enabled=1 AND e.event_type=?)", item.Repo, body.EventType()).Scan(&room)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := s.appendEngineEvent(tx, room, at, body); err != nil {
			return err
		}
	}
	return nil
}
