package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRoomRepos(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []contract.RoomID{"a", "b"} {
		if err := s.CreateRoom(ctx, id, string(id), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		id   contract.RoomID
		repo contract.RepoName
		def  bool
	}{{"a", "one", false}, {"a", "two", true}, {"b", "one", false}} {
		if err := s.AddRoomRepo(ctx, step.id, step.repo, step.def); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RoomRepos(ctx, "a")
	want := []contract.RoomRepo{{Repo: "one"}, {Repo: "two", Default: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	var defaultRoom contract.RoomID
	if err := s.Read().QueryRowContext(ctx, "SELECT default_room FROM repos WHERE repo='one'").Scan(&defaultRoom); err != nil || defaultRoom != "a" {
		t.Fatalf("default room %q: %v", defaultRoom, err)
	}
	if err := s.AddRoomRepo(ctx, "missing", "three", false); err == nil {
		t.Fatal("accepted missing room")
	}
}

func TestRemoveRoomRepoRefusesDefaultAndOpenUses(t *testing.T) {
	ctx := t.Context()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []contract.RepoName{"owner/default", "owner/work"} {
		if err := s.AddRoomRepo(ctx, "room", repo, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,repo) VALUES('ses_worker','room','worker','p','open','',0,'owner/work')`,
		`INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('Owner/Work',4,'ses_worker','','')`,
		`INSERT INTO pr_owners(repo,number,session,goal,approved_head,head_repo) VALUES('upstream/work',6,'ses_worker','','','owner/work')`,
		`INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('owner/work',5,'ses_worker','','','','open')`,
	} {
		if _, err := s.writer.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveRoomRepo(ctx, "room", "owner/default"); !errors.Is(err, ErrRoomRepoInUse) || !strings.Contains(err.Error(), "room default") {
		t.Fatalf("default: %v", err)
	}
	err = s.RemoveRoomRepo(ctx, "room", "owner/work")
	if !errors.Is(err, ErrRoomRepoInUse) {
		t.Fatalf("used: %v", err)
	}
	for _, name := range []string{"session worker (ses_worker)", "PR #4", "PR #6 (upstream/work)", "issue #5"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("refusal does not name %s: %v", name, err)
		}
	}
	if repos, err := s.RoomRepos(ctx, "room"); err != nil || len(repos) != 2 {
		t.Fatalf("repos changed on refusal: %+v %v", repos, err)
	}
}

func TestRemoveRoomRepoPreservesSharedStateUntilLastLink(t *testing.T) {
	ctx := t.Context()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	enabled := true
	const repo contract.RepoName = "owner/shared"
	for _, room := range []contract.RoomID{"a", "b"} {
		if err := s.CreateRoom(ctx, room, string(room), now); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoomRepo(ctx, room, "owner/default", true); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoomRepo(ctx, room, repo, false, &enabled); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRepoCI(ctx, repo, true, 2); err != nil {
		t.Fatal(err)
	}
	pr := TrackedPR{PrSummary: contract.PrSummary{Repo: repo, Number: 4, State: contract.PrOpen}}
	if err := s.ApplyPRPoll(ctx, GitHubPass{
		RoomPRs: []RoomPRPass{{Room: "a", Repo: repo, Rows: []TrackedPR{pr}}, {Room: "b", Repo: repo, Rows: []TrackedPR{pr}}},
		Heads:   []RepoHead{{Repo: repo, Head: "head"}},
		Feeds:   []GitHubFeed{{Repo: repo, Feed: "issues", ETag: "etag"}},
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,closed_at,repo) VALUES('sa','a','worker','p','closed','',0,1,'owner/shared')`,
		`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,repo) VALUES('sb','b','worker','p','open','',0,'owner/default')`,
		`INSERT INTO pr_owners(repo,number,session,goal,approved_head,state) VALUES('owner/shared',4,'sa','','','merged')`,
		`INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('owner/shared',5,'sb','','','','open')`,
	} {
		if _, err := s.writer.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveRoomRepo(ctx, "a", repo); err != nil {
		t.Fatal(err)
	}
	watches, err := s.RoomPRWatches(ctx, repo)
	if err != nil || len(watches) != 1 || watches[0].Room != "b" || len(watches[0].Rows) != 1 {
		t.Fatalf("room snapshots: %+v %v", watches, err)
	}
	runners, err := s.CIRepos(ctx)
	if err != nil || len(runners) != 1 || runners[0].Room != "b" || runners[0].Max != 2 {
		t.Fatalf("shared CI: %+v %v", runners, err)
	}
	if feeds, err := s.GitHubFeeds(ctx); err != nil || feeds[repo]["issues"].ETag != "etag" {
		t.Fatalf("shared feeds: %+v %v", feeds, err)
	}
	if _, err := s.PR(ctx, repo, 4); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("closed tracking entry retained: %v", err)
	}
	if err := s.RemoveRoomRepo(ctx, "b", repo); !errors.Is(err, ErrRoomRepoInUse) {
		t.Fatalf("foreign open tracker was not preserved: %v", err)
	}
	if err := s.UntrackIssue(ctx, repo, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRoomRepo(ctx, "b", repo); !errors.Is(err, ErrRoomRepoInUse) || !strings.Contains(err.Error(), "dyna room repo ci owner/shared --off") {
		t.Fatalf("CI refusal: %v", err)
	}
	if err := s.SetRepoCI(ctx, repo, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRoomRepo(ctx, "b", repo); err != nil {
		t.Fatal(err)
	}
	issue := TrackedIssue{IssueSummary: contract.IssueSummary{Repo: repo, Number: 5, Session: "sb", State: contract.IssueOpen}}
	if err := s.TrackIssue(ctx, issue, true); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("stale issue track: %v", err)
	}
	pr.Session = "sb"
	if err := s.TrackPR(ctx, pr, true); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("stale PR track: %v", err)
	}
	// A start that picked this repo before unlinking must not record a stale session.
	if _, err := s.StartSession(ctx, Session{Room: "b", Repo: repo}, "", contract.BoxSizeAgent, "", contract.Event{}, contract.Event{}, "", false, "", "", 0); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("stale start: %v", err)
	}
	if watches, err := s.RoomPRWatches(ctx, repo); err != nil || len(watches) != 0 {
		t.Fatalf("snapshots remain: %+v %v", watches, err)
	}
	if runners, err := s.CIRepos(ctx); err != nil || len(runners) != 0 {
		t.Fatalf("CI remains: %+v %v", runners, err)
	}
	if feeds, err := s.GitHubFeeds(ctx); err != nil || len(feeds[repo]) != 0 {
		t.Fatalf("feeds remain: %+v %v", feeds, err)
	}
	if heads, err := s.RepoHeads(ctx, now); err != nil || len(heads) != 1 || heads["owner/default"].Head != "" {
		t.Fatalf("heads: %+v %v", heads, err)
	}
}
func TestRemoveRoomRepoPreservesFeedForForeignTracker(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	now := time.Now()
	const auditRepo contract.RepoName = "owner/work"
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []contract.RepoName{"owner/default", auditRepo} {
		if err := s.AddRoomRepo(ctx, "room", repo, false); err != nil {
			t.Fatal(err)
		}
	}
	if e := s.CreateRoom(ctx, "other", "other", now); e != nil {
		t.Fatal(e)
	}
	if _, e := s.writer.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at,repo) VALUES('other-worker','other','worker','p','open','',0,'')`); e != nil {
		t.Fatal(e)
	}
	if e := s.TrackIssue(ctx, TrackedIssue{IssueSummary: contract.IssueSummary{Repo: auditRepo, Number: 5, Session: "other-worker", State: contract.IssueOpen}}, false); e != nil {
		t.Fatal(e)
	}
	if e := s.ApplyPRPoll(ctx, GitHubPass{Feeds: []GitHubFeed{{Repo: auditRepo, Feed: "events", ETag: "still-needed"}}}, now); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveRoomRepo(ctx, "room", auditRepo); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Issue(ctx, auditRepo, 5); e != nil {
		t.Fatal(e)
	}
	feeds, e := s.GitHubFeeds(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if got := feeds[auditRepo]["events"].ETag; got != "still-needed" {
		t.Fatalf("other room's tracker survives but its live feed was reset: %q", got)
	}
}

func TestRepositoryRegistrationMigration(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(ctx, "r", "r", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "r", "owner/repo", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `DROP TABLE repo_registrations; ALTER TABLE boxes DROP COLUMN agy; ALTER TABLE hosts DROP COLUMN seeding; ALTER TABLE hosts DROP COLUMN agy; UPDATE meta SET value='131' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	heads, err := s.RepoHeads(ctx, time.Now())
	if err != nil || heads["owner/repo"].Registration == "" {
		t.Fatalf("missing migrated registration: %+v %v", heads, err)
	}
}

func TestRemoveSharedRoomRepoInvalidatesOldCensus(t *testing.T) {
	ctx := t.Context()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	on := true
	const repo contract.RepoName = "owner/shared"
	for _, room := range []contract.RoomID{"a", "b"} {
		if err := s.CreateRoom(ctx, room, string(room), now); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoomRepo(ctx, room, "owner/default", true); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoomRepo(ctx, room, repo, false, &on); err != nil {
			t.Fatal(err)
		}
	}
	row := TrackedPR{PrSummary: contract.PrSummary{Repo: repo, Number: 1, State: contract.PrOpen}}
	if err := s.ApplyPRPoll(ctx, GitHubPass{
		RoomPRs: []RoomPRPass{{Room: "a", Repo: repo, Rows: []TrackedPR{row}}, {Room: "b", Repo: repo, Rows: []TrackedPR{row}}},
		Feeds:   []GitHubFeed{{Repo: repo, Feed: RoomPRCensusFeed, Cursor: FeedCursor{NextPage: "/page/2", CensusRooms: []contract.RoomID{"a", "b"}}}, {Repo: repo, Feed: "issues", ETag: "live"}},
	}, now); err != nil {
		t.Fatal(err)
	}
	before, err := s.RepoHeads(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	old := GitHubPass{Registrations: map[contract.RepoName]string{repo: before[repo].Registration},
		RoomPRs: []RoomPRPass{{Room: "a", Repo: repo, CensusComplete: true, Rows: []TrackedPR{row}}},
		Feeds:   []GitHubFeed{{Repo: repo, Feed: RoomPRCensusFeed, Cursor: FeedCursor{NextPage: "/page/3", CensusRooms: []contract.RoomID{"a", "b"}}}},
	}
	if err := s.RemoveRoomRepo(ctx, "a", repo); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, "a", repo, false, &on); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPRPoll(ctx, old, now); err != nil {
		t.Fatal(err)
	}
	feeds, err := s.GitHubFeeds(ctx)
	if err != nil || feeds[repo][RoomPRCensusFeed].Cursor.NextPage != "" || feeds[repo]["issues"].ETag != "live" {
		t.Fatalf("census continuation or live cursor changed incorrectly: %+v %v", feeds[repo], err)
	}
	watches, err := s.RoomPRWatches(ctx, repo)
	if err != nil || len(watches) != 2 {
		t.Fatalf("watches: %+v %v", watches, err)
	}
	for _, w := range watches {
		if w.Room == "a" && (w.CensusAt != 0 || len(w.Rows) != 0) {
			t.Fatalf("old pass marked re-added room complete: %+v", w)
		}
		if w.Room == "b" && len(w.Rows) != 1 {
			t.Fatalf("other room's snapshots changed: %+v", w)
		}
	}
}
