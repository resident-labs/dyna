package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/worktrees"
)

// autoTrackSource is the git repository and its tracked worktree. One source
// serves every case of a test because the cases never change its files.
type autoTrackSource struct {
	repo, tree string
}

func newAutoTrackSource(t *testing.T) autoTrackSource {
	t.Helper()
	repo := checkoutRepo(t, "https://github.com/owner/repo.git")
	tree, err := os.MkdirTemp("", "dyna-wt-auto-track-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tree) })
	checkoutGit(t, repo, "worktree", "add", "-b", "feature/auto", tree)
	return autoTrackSource{repo: repo, tree: tree}
}

func autoTrackEnv(t *testing.T) (*testEnv, string) {
	t.Helper()
	return autoTrackEnvIn(t, newAutoTrackSource(t))
}

func autoTrackEnvIn(t *testing.T, source autoTrackSource) (*testEnv, string) {
	t.Helper()
	e := newEnv(t)
	repo, tree := source.repo, source.tree
	if err := worktrees.Track(worktrees.Record{Repo: repo, Path: tree, Branch: "feature/auto", Session: e.author.ID}); err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "work", Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")}
	e.s.Notify = notify.New(rooms, mgr)
	e.author.RoomID, e.author.Room, e.author.Dir = rec.ID, rec.Name, repo
	if _, err := mgr.Store.UpdateMeta(e.author.ID, func(m *subagent.Meta) { m.RoomID = rec.ID; m.Room = rec.Name; m.Dir = repo }); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.Ref = "already-watched"
	e.track()
	pull := e.f.Pull
	pull.Number, pull.Head.Ref = 18, "feature/auto"
	body, _ := json.Marshal(pull)
	e.f.Raw["repos/owner/repo/pulls/18"] = "HTTP/2.0 200 OK\r\n\r\n" + string(body)
	e.f.Discovery = []map[string]any{{"number": 18, "headRefName": "feature/auto", "headRefOid": pull.Head.SHA, "isCrossRepository": false}}
	return e, tree
}

func TestRepoPollAutoTracksOwnedWorktreePR(t *testing.T) {
	// Share the unchanged repository; each case keeps its own sessions and PR store.
	source := newAutoTrackSource(t)
	for _, mode := range []string{"owned", "already-tracked", "closed-record", "fork", "unowned", "closed-owner", "dormant-owner", "wrong-room", "paused-room"} {
		t.Run(mode, func(t *testing.T) {
			e, _ := autoTrackEnvIn(t, source)
			const key = "github.com/owner/repo#18"
			switch mode {
			case "already-tracked", "closed-record":
				if err := e.s.Store.update(func(st *State) error {
					old := *st.Records["github.com/owner/repo#17"]
					old.Key, old.ChangeID, old.Number = key, key, 18
					old.Author = "original-author"
					if mode == "closed-record" {
						old.State, old.Watching = "closed", false
					}
					st.Records[key] = &old
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "fork":
				e.f.Discovery[0]["isCrossRepository"] = true
			case "unowned":
				e.f.Discovery[0]["headRefName"] = "no-owner"
			case "closed-owner":
				if _, err := e.s.Notify.Sessions.Store.UpdateMeta(e.author.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
					t.Fatal(err)
				}
			case "dormant-owner":
				if _, err := e.s.Notify.Sessions.Store.UpdateMeta(e.author.ID, func(m *subagent.Meta) { m.Status = subagent.StatusDormant; m.Finished = time.Now() }); err != nil {
					t.Fatal(err)
				}
			case "paused-room":
				if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
					t.Fatal(err)
				}
			case "wrong-room":
				if _, err := e.s.Notify.Sessions.Store.UpdateMeta(e.author.ID, func(m *subagent.Meta) { m.RoomID = "other" }); err != nil {
					t.Fatal(err)
				}
			}
			e.tick()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			r := st.Records[key]
			switch mode {
			case "owned":
				if r == nil || r.Author != e.author.ID || r.RoomID != e.author.RoomID || !r.Watching {
					t.Fatalf("worktree PR was not tracked with its owner: %+v", r)
				}
				calls, _ := os.ReadFile(e.calls)
				want := "pullRequests(first:1,headRefName:\"feature/auto\",states:OPEN){nodes{number headRefName isCrossRepository headRefOid}}"
				if strings.Count(string(calls), "api --hostname github.com graphql") != 1 || !strings.Contains(string(calls), want) {
					t.Fatalf("discovery did not reuse one bounded batch: %s", calls)
				}
			case "already-tracked", "closed-record":
				if r == nil || r.Author != "original-author" {
					t.Fatalf("existing author replaced: %+v", r)
				}
				if mode == "closed-record" && (r.State != "closed" || r.Watching) {
					t.Fatalf("closed record reopened: %+v", r)
				}
			default:
				if r != nil {
					t.Fatalf("%s must not be tracked: %+v", mode, r)
				}
			}
			if mode == "closed-owner" {
				messages, err := e.s.Notify.Rooms.Messages(e.author.RoomID)
				if err != nil || len(messages) != 0 {
					t.Fatalf("closed owner caused a post: %+v %v", messages, err)
				}
			}
		})
	}
}

func TestAutoTrackUsesSessionWorkingInInheritedWorktree(t *testing.T) {
	e, tree := autoTrackEnv(t)
	child := subagent.Meta{
		ID: "ses_20261026-000001-cccccccc", Name: "child", RoomID: e.author.RoomID, Room: e.author.Room,
		ParentSession: e.author.ID, Dir: tree, Status: subagent.StatusRunning,
	}
	recordtest.Write(t, e.s.Notify.Sessions.Store.Root, child.ID, child)
	e.tick()
	got, err := e.s.Store.Get("github.com/owner/repo#18")
	if err != nil || got.Author != child.ID || got.Dir != tree {
		t.Fatalf("inherited worktree PR author = %q, dir = %q, err = %v; want child %q", got.Author, got.Dir, err, child.ID)
	}
}

func TestAutoTrackFailureDoesNotFailPollOrRepeatAtSameHead(t *testing.T) {
	e, _ := autoTrackEnv(t)
	e.f.Raw["repos/owner/repo/pulls/18"] = "HTTP/2.0 200 OK\r\n\r\ninvalid-json"
	e.tick()
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if got := strings.Count(string(calls), "repos/owner/repo/pulls/18 --include"); got != 1 {
		t.Fatalf("failed tracking attempts=%d, want one", got)
	}
	e.f.Discovery[0]["headRefOid"] = strings.Repeat("b", 40)
	e.tick()
	calls, _ = os.ReadFile(e.calls)
	if got := strings.Count(string(calls), "repos/owner/repo/pulls/18 --include"); got != 2 {
		t.Fatalf("new remote head did not retry once: attempts=%d", got)
	}
	e.f.Discovery[0]["headRefOid"] = strings.Repeat("a", 40)
	e.tick()
	calls, _ = os.ReadFile(e.calls)
	if got := strings.Count(string(calls), "repos/owner/repo/pulls/18 --include"); got != 2 {
		t.Fatalf("returning to an already failed head retried: %d", got)
	}
}

func TestAutoTrackPreservesAttemptUntilItHasABudget(t *testing.T) {
	e, _ := autoTrackEnv(t)
	e.f.Raw["repos/owner/repo/pulls/18"] = "HTTP/2.0 200 OK\r\n\r\ninvalid-json"
	e.write()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	probes, err := e.s.refreshHeads(t.Context(), st, e.clock, nil, e.s.discoveryRecords(t.Context(), st, e.s.loadWorktreeOwners(t.Context(), st))...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	e.s.autoTrack(ctx, st, probes, e.s.loadWorktreeOwners(t.Context(), st))
	e.s.autoTrack(t.Context(), st, probes, e.s.loadWorktreeOwners(t.Context(), st))
	calls, _ := os.ReadFile(e.calls)
	if got := strings.Count(string(calls), "repos/owner/repo/pulls/18 --include"); got != 1 {
		t.Fatalf("budget deferral consumed or repeated the attempt: %d", got)
	}
}

func TestAutoTrackChecksEachCloneRoom(t *testing.T) {
	e, _ := autoTrackEnv(t)
	e.write()
	otherRepo := checkoutRepo(t, "https://github.com/owner/repo.git")
	otherRoom, err := e.s.Notify.Rooms.Create(room.CreateInput{Name: "other-clone", Repo: otherRepo})
	if err != nil {
		t.Fatal(err)
	}
	const first = "github.com/owner/repo#17"
	const second = "github.com/owner/repo#19"
	if err := e.s.Store.update(func(st *State) error {
		owned := *st.Records[first]
		owned.Key, owned.ChangeID, owned.Number = second, second, 19
		st.Records[second] = &owned
		st.Records[first].RepoPath = otherRepo
		st.Records[first].RoomID = otherRoom.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	pulls := []discoveredPull{{Number: 18, Branch: "feature/auto", Head: e.f.Pull.Head.SHA}}
	e.s.autoTrack(t.Context(), st, map[string]headProbe{
		first: {OpenPulls: pulls}, second: {OpenPulls: pulls},
	}, e.s.loadWorktreeOwners(t.Context(), st))
	got, err := e.s.Store.Get("github.com/owner/repo#18")
	if err != nil || got.Author != e.author.ID || got.RoomID != e.author.RoomID {
		t.Fatalf("second clone owner was skipped: %+v %v", got, err)
	}
}

func TestRepoPollDiscoversWorktreeWithoutWatchedPR(t *testing.T) {
	e, _ := autoTrackEnv(t)
	if err := e.s.Store.update(func(st *State) error {
		st.Records = map[string]*Record{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	got, err := e.s.Store.Get("github.com/owner/repo#18")
	if err != nil || got.Author != e.author.ID || got.RoomID != e.author.RoomID {
		t.Fatalf("first PR was not tracked with its worktree owner: author=%s room=%s err=%v", got.Author, got.RoomID, err)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "api --hostname github.com graphql") != 1 ||
		!strings.Contains(string(calls), `headRefName:"feature/auto"`) || strings.Contains(string(calls), "pullRequests(first:20") {
		t.Fatalf("discovery must query only the owned branch in one batch: %s", calls)
	}
}

func TestRepoPollBatchesUntrackedWorktreeBranches(t *testing.T) {
	e, _ := autoTrackEnv(t)
	tree, err := os.MkdirTemp("", "dyna-wt-discovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tree) })
	checkoutGit(t, e.author.Dir, "worktree", "add", "-b", "feature/second", tree)
	if err := worktrees.Track(worktrees.Record{Repo: e.author.Dir, Path: tree, Branch: "feature/second", Session: e.author.ID}); err != nil {
		t.Fatal(err)
	}
	pull := e.f.Pull
	pull.Number, pull.Head.Ref = 20, "feature/second"
	body, _ := json.Marshal(pull)
	e.f.Raw["repos/owner/repo/pulls/20"] = "HTTP/2.0 200 OK\r\n\r\n" + string(body)
	e.f.Discovery = append(e.f.Discovery, map[string]any{"number": 20, "headRefName": pull.Head.Ref, "headRefOid": pull.Head.SHA, "isCrossRepository": false})
	if err := e.s.Store.update(func(st *State) error { st.Records = map[string]*Record{}; return nil }); err != nil {
		t.Fatal(err)
	}
	e.tick()
	for _, key := range []string{"github.com/owner/repo#18", "github.com/owner/repo#20"} {
		got, err := e.s.Store.Get(key)
		if err != nil || got.Author != e.author.ID {
			t.Fatalf("%s not discovered: author=%s err=%v", key, got.Author, err)
		}
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "api --hostname github.com graphql") != 1 {
		t.Fatalf("branch queries were not batched: %s", calls)
	}
	e.tick()
	calls, _ = os.ReadFile(e.calls)
	if strings.Count(string(calls), "pullRequests(first:1,") != 2 {
		t.Fatalf("already tracked branches were queried again: %s", calls)
	}
}

func TestRepoPollDiscoversReusedWorktreeBranch(t *testing.T) {
	for _, state := range []string{"closed", "merged", "open-unwatched"} {
		t.Run(state, func(t *testing.T) {
			e, _ := autoTrackEnv(t)
			if err := e.s.Store.update(func(st *State) error {
				old := st.Records["github.com/owner/repo#17"]
				old.Branch, old.State, old.Watching = "feature/auto", state, false
				if state == "open-unwatched" {
					old.State = "open"
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.tick()
			got, err := e.s.Store.Get("github.com/owner/repo#18")
			if err != nil || got.Author != e.author.ID {
				t.Fatalf("new PR on reused branch was skipped: author=%s err=%v", got.Author, err)
			}
			old, err := e.s.Store.Get("github.com/owner/repo#17")
			want := state
			if state == "open-unwatched" {
				want = "open"
			}
			if err != nil || old.State != want || old.Watching {
				t.Fatalf("historical PR changed: state=%s watching=%v err=%v", old.State, old.Watching, err)
			}
		})
	}
}

func TestDiscoverySkipsHistoricalOwnersAndMainTrees(t *testing.T) {
	e, _ := autoTrackEnv(t)
	const closedOwners, closedTrees = 12, 6
	for i := 0; i < closedOwners; i++ {
		id := fmt.Sprintf("ses_20261017-010000-%08x", i)
		recordtest.Write(t, e.s.Notify.Sessions.Store.Root, id, subagent.Meta{ID: id, RoomID: e.author.RoomID, Closed: time.Now()})
		if i < closedTrees {
			name := fmt.Sprintf("dyna-wt-closed-%d", i)
			rec := worktrees.Record{Repo: e.author.Dir, Path: filepath.Join(os.TempDir(), name), Branch: fmt.Sprintf("closed-%d", i), Session: id}
			if err := worktrees.Track(rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A main checkout can have a non-default current branch.
	if err := worktrees.Track(worktrees.Record{Root: filepath.Dir(e.author.Dir), Repo: e.author.Dir, Path: e.author.Dir, Branch: "main", Session: e.author.ID}); err != nil {
		t.Fatal(err)
	}
	tree, err := os.MkdirTemp("", "dyna-wt-default-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tree) })
	if err := worktrees.Track(worktrees.Record{Repo: e.author.Dir, Path: tree, Branch: "release", Session: e.author.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#17"].DefaultBranch = "release"; return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	inputs := e.s.discoveryRecords(t.Context(), st, e.s.loadWorktreeOwners(t.Context(), st))
	if len(inputs) != 1 || inputs[0].Branch != "feature/auto" {
		var branches []string
		for _, r := range inputs {
			branches = append(branches, r.Branch)
		}
		t.Fatalf("unexpected discovery queries: %v", branches)
	}
	e.write()
	if _, _, err := e.s.GH.readHeads(t.Context(), inputs); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Count(string(calls), "headRefName:") != 1 || !strings.Contains(string(calls), `headRefName:"feature/auto"`) {
		t.Fatalf("excluded trees reached the query: %s", calls)
	}
}
