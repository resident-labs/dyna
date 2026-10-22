package watch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/pr/state"
)

// radarRepo commits a base file and one branch per edit. Each edit replaces
// one line of the base file.
func radarRepo(t *testing.T, edits map[string][2]string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	changeGit(t, dir, "init", "-q", "-b", "main")
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		changeGit(t, dir, "add", "f.txt")
	}
	base := "one\ntwo\nthree\nfour\nfive\n"
	write(base)
	changeGit(t, dir, "commit", "-q", "-m", "base")
	heads := map[string]string{}
	for branch, edit := range edits {
		changeGit(t, dir, "checkout", "-q", "-b", branch, "main")
		write(strings.Replace(base, edit[0], edit[1], 1))
		changeGit(t, dir, "commit", "-q", "-m", branch)
		heads[branch] = changeGit(t, dir, "rev-parse", "HEAD")
	}
	return dir, heads
}

func radarRecords(t *testing.T, e *testEnv, dir string, heads map[string]string, edit func(a, b *Record)) (Record, Record) {
	t.Helper()
	a := Record{Key: "github.com/owner/repo#1", Host: "github.com", Repo: "owner/repo", Number: 1, Branch: "a", BaseRef: "main", Head: heads["a"], RepoPath: dir, RoomID: e.author.RoomID, Author: "ses_20261017-000003-cccccccc", State: "open", Watching: true}
	b := Record{Key: "github.com/owner/repo#2", Host: "github.com", Repo: "owner/repo", Number: 2, Branch: "b", BaseRef: "main", Head: heads["b"], RepoPath: dir, RoomID: e.author.RoomID, Author: "ses_20261017-000004-dddddddd", State: "open", Watching: true}
	if edit != nil {
		edit(&a, &b)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[a.Key], st.Records[b.Key] = &a, &b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func radarNotices(r *Record) []Notice {
	var out []Notice
	for _, n := range r.Notices {
		if strings.Contains(n.Text, "change the same lines") {
			out = append(out, n)
		}
	}
	return out
}

// Both PRs are queued by change ID, so a goes first and only b must rebase.
func TestRadarConflictNotifiesOnlyTheAuthorBehindOnce(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	for range 2 {
		if err := e.s.refreshOverlaps(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := e.s.Store.Read()
	for _, pair := range [][2]Record{{a, b}, {b, a}} {
		r := st.Records[pair[0].Key]
		o, ok := overlapWith(*r, changeID(pair[1]))
		if !ok || !slices.Equal(o.Conflicts, []string{"f.txt"}) || len(o.Files) != 0 || o.Error != "" {
			t.Fatalf("%s overlap = %+v (%v)", r.Key, o, ok)
		}
	}
	if notices := radarNotices(st.Records[a.Key]); len(notices) != 0 {
		t.Fatalf("the author ahead got notices: %+v", notices)
	}
	notices := radarNotices(st.Records[b.Key])
	if len(notices) != 1 || notices[0].Target != b.Author || !strings.Contains(notices[0].Text, "f.txt") || !strings.Contains(notices[0].Text, "rebase PR #2") {
		t.Fatalf("behind notices = %+v", notices)
	}
	for _, item := range mergeQueue(st) {
		if len(item.Overlaps) != 1 {
			t.Fatalf("merge status omits the overlap: %+v", item)
		}
	}
}

// A notice is sent again only when a head or the queue order changes, and
// only to the author who must rebase.
func TestRadarResendsOnlyWhenAHeadOrQueueOrderChanges(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	refresh := func(edit func(st *State)) (aNotices, bNotices []Notice) {
		t.Helper()
		if err := e.s.Store.update(func(st *State) error { edit(st); return nil }); err != nil {
			t.Fatal(err)
		}
		if err := e.s.refreshOverlaps(t.Context()); err != nil {
			t.Fatal(err)
		}
		st, _ := e.s.Store.Read()
		return radarNotices(st.Records[a.Key]), radarNotices(st.Records[b.Key])
	}
	pending := func(ns []Notice) int {
		n := 0
		for _, x := range ns {
			if !x.Done && x.SupersededBy == "" {
				n++
			}
		}
		return n
	}
	if an, bn := refresh(func(*State) {}); len(an) != 0 || len(bn) != 1 {
		t.Fatalf("first conflict: a=%d b=%d notices, want 0 and 1", len(an), len(bn))
	}

	// Main moves without touching f.txt: the pair is compared again, unchanged.
	changeGit(t, dir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "add", "g.txt")
	changeGit(t, dir, "commit", "-q", "-m", "main moves")
	main := changeGit(t, dir, "rev-parse", "HEAD")
	if an, bn := refresh(func(st *State) {
		st.RepositoryFacts = map[string]state.RepositoryFacts{"github.com/owner/repo": {DefaultBranch: "main", Head: main}}
	}); len(an) != 0 || len(bn) != 1 {
		t.Fatalf("main moved: a=%d b=%d notices, want 0 and 1", len(an), len(bn))
	}

	// The PR behind pushes and still conflicts: only its author hears again.
	changeGit(t, dir, "checkout", "-q", "b")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo from b again\nthree\nfour\nfive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "commit", "-q", "-am", "b again")
	pushed := changeGit(t, dir, "rev-parse", "HEAD")
	if an, bn := refresh(func(st *State) { st.Records[b.Key].Head = pushed }); len(an) != 0 || len(bn) != 2 {
		t.Fatalf("b pushed: a=%d b=%d notices, want 0 and 2", len(an), len(bn))
	}

	// b enters a merge trial and goes first: a must rebase now, and b's
	// undelivered notices are wrong.
	trial := func(st *State) {
		st.MergeTrials = map[string]*state.MergeTrial{"github.com/owner/repo": {Branch: "dyna/merge-trial/1", Changes: []state.MergeTrialChange{{Key: b.Key, Head: pushed}}}}
	}
	an, bn := refresh(trial)
	if len(an) != 1 || an[0].Target != a.Author || !strings.Contains(an[0].Text, "rebase PR #1") || pending(bn) != 0 {
		t.Fatalf("order flipped: a=%+v b pending=%d", an, pending(bn))
	}
	if an, bn := refresh(trial); len(an) != 1 || len(bn) != 2 {
		t.Fatalf("unchanged tick resent: a=%d b=%d notices, want 1 and 2", len(an), len(bn))
	}

	// The trial is cancelled and a goes first again: b's notice for the
	// current heads is pending again, and a's undelivered one is dropped.
	an, bn = refresh(func(st *State) { st.MergeTrials = nil })
	if len(an) != 1 || pending(an) != 0 || len(bn) != 2 || pending(bn) != 1 {
		t.Fatalf("order flipped back: a=%d (pending %d) b=%d (pending %d), want 1 (0) and 2 (1)", len(an), pending(an), len(bn), pending(bn))
	}
}

func TestRadarSharedFileWithoutConflictSendsNoNotice(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"one", "one from a"}, "b": {"five", "five from b"}})
	a, _ := radarRecords(t, e, dir, heads, nil)
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	r := st.Records[a.Key]
	o, ok := overlapWith(*r, "github.com/owner/repo#2")
	if !ok || !slices.Equal(o.Files, []string{"f.txt"}) || len(o.Conflicts) != 0 || len(radarNotices(r)) != 0 {
		t.Fatalf("overlap = %+v notices = %+v", o, r.Notices)
	}
}

func TestRadarSkipsGitForUnchangedHeads(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, _ := radarRecords(t, e, dir, heads, nil)
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Git fails without the repository, so a second git pass would save an error.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if o, ok := overlapWith(*st.Records[a.Key], "github.com/owner/repo#2"); !ok || o.Error != "" || len(o.Conflicts) != 1 {
		t.Fatalf("unchanged pair ran git again: %+v", o)
	}
}

func TestRadarSkipsStackedPairs(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	for name, edit := range map[string]func(a, b *Record){
		"base branch": func(_, b *Record) { b.BaseRef = "a" },
		"dependency":  func(a, b *Record) { b.Dependencies = []string{a.Key} },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := radarRecords(t, e, dir, heads, edit)
			if err := e.s.refreshOverlaps(t.Context()); err != nil {
				t.Fatal(err)
			}
			st, _ := e.s.Store.Read()
			if len(st.Records[a.Key].Overlaps) != 0 || len(st.Records[b.Key].Overlaps) != 0 {
				t.Fatalf("stacked pair compared: %+v %+v", st.Records[a.Key].Overlaps, st.Records[b.Key].Overlaps)
			}
		})
	}
}

func TestRadarRetriesFailedComparisons(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	away := dir + ".away"
	if err := os.Rename(dir, away); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if o, _ := overlapWith(*st.Records[a.Key], changeID(b)); o.Error == "" {
		t.Fatalf("missing repository did not fail the comparison: %+v", o)
	}
	if err := os.Rename(away, dir); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ = e.s.Store.Read()
	if live := st.Records[b.Key]; len(radarNotices(live)) != 1 {
		t.Fatalf("%s was not compared again after the failure cleared: %+v", b.Key, live.Overlaps)
	}
}

// Main's own changes between two PR bases are not either PR's change. Here
// main changed a line after b branched, so b conflicts with main, and a (from
// the new main) touches another file. The pair has no conflict of its own.
func TestRadarComparesEachPRAgainstMain(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	changeGit(t, dir, "init", "-q", "-b", "main")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		changeGit(t, dir, "add", name)
	}
	write("f.txt", "one\ntwo\nthree\n")
	changeGit(t, dir, "commit", "-q", "-m", "base")
	changeGit(t, dir, "checkout", "-q", "-b", "b")
	write("f.txt", "one\ntwo from b\nthree\n")
	changeGit(t, dir, "commit", "-q", "-m", "b")
	changeGit(t, dir, "checkout", "-q", "main")
	write("f.txt", "one\ntwo from main\nthree\n")
	changeGit(t, dir, "commit", "-q", "-m", "main moves")
	main := changeGit(t, dir, "rev-parse", "HEAD")
	changeGit(t, dir, "checkout", "-q", "-b", "a")
	write("g.txt", "a\n")
	changeGit(t, dir, "commit", "-q", "-m", "a")
	heads := map[string]string{"a": changeGit(t, dir, "rev-parse", "a"), "b": changeGit(t, dir, "rev-parse", "b")}
	a, b := radarRecords(t, e, dir, heads, nil)
	if err := e.s.Store.update(func(st *State) error {
		if st.RepositoryFacts == nil {
			st.RepositoryFacts = map[string]state.RepositoryFacts{}
		}
		st.RepositoryFacts["github.com/owner/repo"] = state.RepositoryFacts{DefaultBranch: "main", Head: main}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	o, _ := overlapWith(*st.Records[a.Key], changeID(b))
	if len(o.Conflicts) != 0 || len(o.Files) != 0 || len(radarNotices(st.Records[a.Key])) != 0 {
		t.Fatalf("main's change was counted as a's change: %+v", o)
	}
}

func TestRadarFindsMainRelativeConflict(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	main := changeGit(t, dir, "rev-parse", "main")
	if err := e.s.Store.update(func(st *State) error {
		if st.RepositoryFacts == nil {
			st.RepositoryFacts = map[string]state.RepositoryFacts{}
		}
		st.RepositoryFacts["github.com/owner/repo"] = state.RepositoryFacts{DefaultBranch: "main", Head: main}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if o, _ := overlapWith(*st.Records[a.Key], changeID(b)); !slices.Equal(o.Conflicts, []string{"f.txt"}) || o.MainHead != main || o.Error != "" {
		t.Fatalf("overlap = %+v", o)
	}
}

func TestRadarSkipsPRsBasedOnAnotherBranch(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, func(a, b *Record) {
		a.DefaultBranch, b.DefaultBranch = "main", "main"
		b.BaseRef = "untracked-feature"
	})
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if len(st.Records[a.Key].Overlaps) != 0 || len(st.Records[b.Key].Overlaps) != 0 {
		t.Fatalf("a PR based on another branch was compared: %+v %+v", st.Records[a.Key].Overlaps, st.Records[b.Key].Overlaps)
	}
}

func TestRadarDropsUndeliveredNoticeWhenPairNoLongerConflicts(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"one", "one from a"}, "b": {"five", "five from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[a.Key]
		notice(live, live.Author, changeLabel(*live)+" and "+changeLabel(b)+" change the same lines in f.txt. "+changeLabel(b)+" is ahead in the merge queue.", "radar:"+b.Key+":old:old")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	for _, n := range st.Records[a.Key].Notices {
		if n.Kind == "radar" && n.SupersededBy != "radar-cleared" {
			t.Fatalf("false radar notice is still pending: %+v", n)
		}
	}
}

// A and B replace one line differently. While main also changes that line,
// both conflict with main and the pair has no conflict of its own. After main
// reverts, the unchanged pair conflicts and must be found again.
func TestRadarRechecksPairsWhenMainMoves(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	changeGit(t, dir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo from main\nthree\nfour\nfive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, dir, "commit", "-q", "-am", "main edits")
	edited := changeGit(t, dir, "rev-parse", "HEAD")
	changeGit(t, dir, "revert", "--no-edit", "HEAD")
	reverted := changeGit(t, dir, "rev-parse", "HEAD")
	setMain := func(head string) {
		t.Helper()
		if err := e.s.Store.update(func(st *State) error {
			if st.RepositoryFacts == nil {
				st.RepositoryFacts = map[string]state.RepositoryFacts{}
			}
			st.RepositoryFacts["github.com/owner/repo"] = state.RepositoryFacts{DefaultBranch: "main", Head: head}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.refreshOverlaps(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	setMain(edited)
	st, _ := e.s.Store.Read()
	if o, _ := overlapWith(*st.Records[a.Key], changeID(b)); len(o.Conflicts) != 0 {
		t.Fatalf("pair conflict while both conflict with main: %+v", o)
	}
	setMain(reverted)
	st, _ = e.s.Store.Read()
	if o, _ := overlapWith(*st.Records[a.Key], changeID(b)); !slices.Equal(o.Conflicts, []string{"f.txt"}) {
		t.Fatalf("pair was not checked again after main reverted: %+v", o)
	}
}

// A cleared notice is found by its pair, not by the change label in its text,
// so a reworded conflict line still clears and a label in another pair's text
// does not.
func TestRadarClearsPairNoticeByIdentityNotText(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"one", "one from a"}, "b": {"five", "five from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	const reworded = "The same lines changed in two pull requests."
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[a.Key]
		notice(live, live.Author, reworded, "radar:"+b.Key+":old:old")
		// This notice names the pair in its text but belongs to another change.
		notice(live, live.Author, changeLabel(*live)+" and "+changeLabel(b)+" change the same lines.", "radar:github.com/owner/repo#99:old:old")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	for _, n := range st.Records[a.Key].Notices {
		if n.Kind != "radar" {
			continue
		}
		if strings.Contains(n.Text, reworded) {
			if n.SupersededBy != "radar-cleared" {
				t.Fatalf("notice for the pair is still pending: %+v", n)
			}
			continue
		}
		if n.SupersededBy != "" {
			t.Fatalf("notice for another change was cleared: %+v", n)
		}
	}
}

// A notice saved before the pair identity was stored still clears when its
// pair stops conflicting. The regression seeds the old shape directly: no
// Other, only the change label in the text.
func TestRadarClearsStoredNoticeWithoutPairIdentity(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"one", "one from a"}, "b": {"five", "five from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	const oldID = "msg_20261017-000000-radarold"
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[a.Key]
		live.Notices = append(live.Notices, Notice{ID: oldID, Kind: "radar", Target: live.Author,
			Text: changeLabel(*live) + " and " + changeLabel(b) + " change the same lines in f.txt. " + changeLabel(b) + " is ahead in the merge queue."})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	for _, n := range st.Records[a.Key].Notices {
		if n.ID == oldID {
			if n.SupersededBy != "radar-cleared" {
				t.Fatalf("stored notice for the pair is still pending: %+v", n)
			}
			return
		}
	}
	t.Fatalf("stored notice is gone: %+v", st.Records[a.Key].Notices)
}

// A stored notice without the pair identity is repaired when its pair
// conflicts again, so the next pass clears it by identity.
func TestRadarBackfillsPairIdentityOnStoredNotice(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, nil)
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Drop the identity to the shape an older release saved.
	if err := e.s.Store.update(func(st *State) error {
		for i := range st.Records[b.Key].Notices {
			st.Records[b.Key].Notices[i].Other = ""
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	pending := radarNotices(st.Records[b.Key])
	if len(pending) == 0 {
		t.Fatalf("no radar notice for the conflicting pair: %+v", st.Records[b.Key].Notices)
	}
	for _, n := range pending {
		if n.Other != a.Key {
			continue
		}
		return
	}
	for _, n := range pending {
		t.Fatalf("stored notice still has no pair identity: %+v", n)
	}
}

// A pair git does not recompute still carries the name of the other change:
// an overlap saved before titles existed, and a title-only rename with both
// heads unchanged, both reach the chip that names a change with no PR number.
func TestRadarRefreshesTitlesOnCachedPairs(t *testing.T) {
	e := newEnv(t)
	dir, heads := radarRepo(t, map[string][2]string{"a": {"two", "two from a"}, "b": {"two", "two from b"}})
	a, b := radarRecords(t, e, dir, heads, func(a, b *Record) {
		a.Title, b.Title = "First change", "Second change"
	})
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	overlaps := func(t *testing.T) *State {
		t.Helper()
		st, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		return &st
	}
	named := func(t *testing.T, st *State, key, want string) {
		t.Helper()
		kept := st.Records[key].Overlaps
		if len(kept) != 1 || kept[0].Title != want {
			t.Fatalf("%s overlaps after a cached pass: %+v", key, kept)
		}
	}
	// An overlap stored before the title field existed loses its name.
	if err := e.s.Store.update(func(st *State) error {
		for _, key := range []string{a.Key, b.Key} {
			for i := range st.Records[key].Overlaps {
				st.Records[key].Overlaps[i].Title = ""
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Both heads and main are unchanged, so the next pass runs no git and only
	// the retained pair can restore the names. Removing the repository proves it.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := overlaps(t)
	named(t, st, a.Key, "Second change")
	named(t, st, b.Key, "First change")
	// A rename changes no head, so only the retained overlap carries it.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[b.Key].Title = "Renamed change"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshOverlaps(t.Context()); err != nil {
		t.Fatal(err)
	}
	named(t, overlaps(t), a.Key, "Renamed change")
}
