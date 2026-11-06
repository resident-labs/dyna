package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func branchStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CreateRoom(ctx, "room", "room", time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,repo) VALUES(?,'room',?,'p','ready','',0,'owner/repo')", id, id); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

func TestClaimBranchGivesTheWantedNameAndKeepsIt(t *testing.T) {
	s, ctx := branchStore(t)
	got, err := s.ClaimBranch(ctx, "owner/repo", "a", "exec-only-usage", nil, time.Unix(1, 0))
	if err != nil || got != "exec-only-usage" {
		t.Fatalf("got %q, %v", got, err)
	}
	again, err := s.ClaimBranch(ctx, "owner/repo", "a", "something-else", nil, time.Unix(2, 0))
	if err != nil || again != "exec-only-usage" {
		t.Fatalf("a second claim by the same session gave %q, %v", again, err)
	}
}

func TestClaimBranchAddsASuffixOnARemoteBranchOrALiveClaim(t *testing.T) {
	s, ctx := branchStore(t)
	remote := map[string]bool{"work": true}
	got, err := s.ClaimBranch(ctx, "owner/repo", "a", "work", remote, time.Unix(1, 0))
	if err != nil || got != "work-2" {
		t.Fatalf("remote clash: %q, %v", got, err)
	}
	got, err = s.ClaimBranch(ctx, "owner/repo", "b", "work", remote, time.Unix(2, 0))
	if err != nil || got != "work-3" {
		t.Fatalf("remote and live clash: %q, %v", got, err)
	}
	got, err = s.ClaimBranch(ctx, "owner/repo", "c", "work-2", nil, time.Unix(3, 0))
	if err != nil || got != "work-2-2" {
		t.Fatalf("live claim: %q, %v", got, err)
	}
	other, err := s.ClaimBranch(ctx, "owner/other", "c", "work-2", nil, time.Unix(4, 0))
	if err != nil || other != "work-2" {
		t.Fatalf("another repository: %q, %v", other, err)
	}
}

func TestAClosedSessionsBranchIsFreeAgainButStaysItsOwn(t *testing.T) {
	s, ctx := branchStore(t)
	if _, err := s.ClaimBranch(ctx, "owner/repo", "a", "work", nil, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE sessions SET state='closed',closed_at=5 WHERE id='a'"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimBranch(ctx, "owner/repo", "b", "work", nil, time.Unix(2, 0))
	if err != nil || got != "work" {
		t.Fatalf("got %q, %v", got, err)
	}
	if kept, err := s.SessionBranches(ctx, "owner/repo", "a"); err != nil || !reflect.DeepEqual(kept, []string{"work"}) {
		t.Fatalf("the closed session's rows: %v, %v", kept, err)
	}
	if holder, err := s.BranchHolder(ctx, "owner/repo", "work"); err != nil || holder != "b" {
		t.Fatalf("holder %q, %v", holder, err)
	}
}

func TestAddBranchMakesTheNewestTheSessionsBranchAndKeepsTheOld(t *testing.T) {
	s, ctx := branchStore(t)
	if _, err := s.ClaimBranch(ctx, "owner/repo", "a", "old", nil, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBranch(ctx, "owner/repo", "a", "new", time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionBranches(ctx, "owner/repo", "a"); err != nil || !reflect.DeepEqual(got, []string{"new", "old"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := s.ClaimBranch(ctx, "owner/repo", "b", "taken", nil, time.Unix(3, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBranch(ctx, "owner/repo", "a", "taken", time.Unix(4, 0)); err != ErrBranchTaken {
		t.Fatalf("a branch another session holds: %v", err)
	}
}

// TestUpgradeRecordsEarlierSessionsBranches turns a current store back into version 89, whose sessions have no record, and
// opens it: every session with a repository gets the branch it already had.
func TestUpgradeRecordsEarlierSessionsBranches(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE session_branches",
		"DROP TABLE workflow_settings",
		"ALTER TABLE hosts DROP COLUMN gpus",
		"ALTER TABLE boxes DROP COLUMN gpu_gib",
		"ALTER TABLE boxes DROP COLUMN gpu_index",
		"ALTER TABLE boxes DROP COLUMN create_retry_host",
		"ALTER TABLE rooms DROP COLUMN github_writes",
		"DROP TABLE service_leftovers",
		"DROP TABLE service_secrets",
		"DROP TABLE services",
		"DROP TABLE service_image_sizes",
		"DROP TABLE repo_heads",
		"INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES ('room','room',1,0,0)",
		"INSERT INTO sessions(id,room,name,profile,state,goal,created_at,repo) VALUES ('ses_with','room','with','p','ready','',7,'owner/repo'),('ses_without','room','without','p','ready','',8,'')",
		"UPDATE meta SET value='89' WHERE key='schema_version'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if got, err := s.SessionBranches(ctx, "owner/repo", "ses_with"); err != nil || !reflect.DeepEqual(got, []string{"v2/ses_with"}) {
		t.Fatalf("session with a repository: %v, %v", got, err)
	}
	var rows, at int
	if err := s.Read().QueryRow("SELECT count(*), max(created_at) FROM session_branches").Scan(&rows, &at); err != nil || rows != 1 || at != 7 {
		t.Fatalf("%d rows, created_at %d, %v", rows, at, err)
	}
	// Opening again at the current version records nothing twice.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT count(*) FROM session_branches").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("after a reopen: %d rows, %v", rows, err)
	}
}
