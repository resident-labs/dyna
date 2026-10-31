package engine

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func TestExpireReplayKeys(t *testing.T) {
	s, _, _, ctx := setup(t)
	now := time.Unix(1000000, 0)
	s.now = func() time.Time { return now }
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for id, at := range map[string]time.Time{
			"old":      now.Add(-24*time.Hour - time.Millisecond),
			"boundary": now.Add(-24 * time.Hour),
			"recent":   now.Add(-time.Hour),
		} {
			if _, err := tx.ExecContext(ctx, "INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES('start','session','s',?,'ref',?)", id, at.UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireArchives(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.store.Read().QueryContext(ctx, "SELECT client_id FROM client_replays ORDER BY client_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "boundary" || ids[1] != "recent" {
		t.Fatalf("remaining replay keys = %v", ids)
	}
}

func TestExpireArchives(t *testing.T) {
	s, room, session, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000000, 0)
	s.now = func() time.Time { return now }
	repos := &fakeRepos{err: errors.New("offline")}
	s.SetRepositories(repos)
	old := store.Archive{Session: session, Repo: "owner/repo", Ref: "refs/dyna/archive/" + string(session), Commit: "abc", CreatedAt: now.Add(-15 * 24 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if err := s.store.SaveArchives(ctx, []store.Archive{old}); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireArchives(ctx); err == nil {
		t.Fatal("failed delete not reported")
	}
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives").Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed delete lost archive: %d %v", count, err)
	}
	repos.err = nil
	now = now.Add(time.Hour)
	if err := s.ExpireArchives(ctx); err != nil {
		t.Fatal(err)
	}
	if repos.deleted != old.Ref {
		t.Fatalf("deleted %q, want %q", repos.deleted, old.Ref)
	}
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM archives").Scan(&count); err != nil || count != 0 {
		t.Fatalf("archive remains: %d %v", count, err)
	}
	old.ExpiresAt = now.Add(time.Second)
	if err := s.store.SaveArchives(ctx, []store.Archive{old}); err != nil {
		t.Fatal(err)
	}
	repos.deleted = ""
	if err := s.ExpireArchives(ctx); err != nil || repos.deleted != "" {
		t.Fatalf("unexpired archive removed: %q %v", repos.deleted, err)
	}
	old.ExpiresAt = now
	old.Ref = "" // An archive skipped because the branch was pushed is not a ref to delete.
	if err := s.store.SaveArchives(ctx, []store.Archive{old}); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireArchives(ctx); err != nil || repos.deleted != "" {
		t.Fatalf("skipped archive: %q %v", repos.deleted, err)
	}
}
