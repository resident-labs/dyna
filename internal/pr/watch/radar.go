package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
)

// refreshOverlaps compares each pair of open changes in a repository. Pairs
// whose heads did not move keep a successful result and run no git command;
// failed comparisons run again on the next tick.
// A line-level conflict sends one notice for each pair of heads, only to the
// author of the change behind in the merge queue, who must rebase.
func (s *Service) refreshOverlaps(ctx context.Context) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	groups := map[string][]Record{}
	for _, r := range st.Records {
		if r.Watching && r.State == "open" && commitID(r.Head) && r.RepoPath != "" && !basedOnBranch(*r) {
			groups[defaultRepo(*r)] = append(groups[defaultRepo(*r)], *r)
		}
	}
	computed := map[string]map[string]state.Overlap{}
	put := func(r Record, o state.Overlap) {
		if computed[r.Key] == nil {
			computed[r.Key] = map[string]state.Overlap{}
		}
		computed[r.Key][o.ChangeID] = o
	}
	for repo, rs := range groups {
		main := st.RepositoryFacts[repo].Head
		sort.Slice(rs, func(i, j int) bool { return rs[i].Key < rs[j].Key })
		for i, a := range rs {
			for _, b := range rs[i+1:] {
				if stacked(a, b) {
					continue
				}
				// Every result depends on main as well: a main edit or revert
				// can create or remove a pair conflict with unchanged heads.
				if old, ok := overlapWith(a, changeID(b)); ok && old.Error == "" && old.Head == a.Head && old.OtherHead == b.Head && old.MainHead == main {
					continue
				}
				if ctx.Err() != nil {
					break
				}
				files, conflicts, err := s.overlapPaths(ctx, a, b, main)
				o := state.Overlap{ChangeID: changeID(b), Title: b.Title, Number: b.Number, Files: files, Conflicts: conflicts, Head: a.Head, OtherHead: b.Head, MainHead: main}
				if err != nil {
					o.Error = Sanitize(err.Error())
				}
				put(a, o)
				put(b, state.Overlap{ChangeID: changeID(a), Title: a.Title, Number: a.Number, Files: files, Conflicts: conflicts, Head: b.Head, OtherHead: a.Head, MainHead: main, Error: o.Error})
			}
		}
	}
	return s.Store.update(func(cur *State) error {
		open := map[string]*Record{}
		for _, r := range cur.Records {
			if r.Watching && r.State == "open" {
				open[changeID(*r)] = r
			}
		}
		order := map[string]int{}
		for i, item := range mergeQueue(*cur, s.LocalTrialRooms(ctx)) {
			order[item.ChangeID] = i
		}
		for _, r := range cur.Records {
			if open[changeID(*r)] != r || basedOnBranch(*r) {
				r.Overlaps = nil
				continue
			}
			var kept []state.Overlap
			for _, o := range r.Overlaps {
				if other := open[o.ChangeID]; other != nil && defaultRepo(*other) == defaultRepo(*r) && !stacked(*r, *other) && !basedOnBranch(*other) {
					// A pair that git did not recompute still needs the title the
					// chip names it by, in case the other change was renamed.
					o.Title = other.Title
					kept = append(kept, o)
				}
			}
			for id, o := range computed[r.Key] {
				other := open[id]
				if other == nil || o.Head != r.Head || o.OtherHead != other.Head {
					continue
				}
				kept = slices.DeleteFunc(kept, func(old state.Overlap) bool { return old.ChangeID == id })
				kept = append(kept, o)
			}
			// Every kept pair is checked, not only recomputed ones, so a queue
			// order change with unchanged heads reaches the author now behind.
			// The notice ID holds both heads, so a pair is sent once per head pair.
			// A failed comparison proves nothing, so it changes no notice.
			for _, o := range kept {
				other := open[o.ChangeID]
				if o.Error != "" || o.Head != r.Head || o.OtherHead != other.Head {
					continue
				}
				if len(o.Conflicts) > 0 && !aheadOf(*r, *other, order) {
					notice(r, r.Author, conflictLine(*other, *r, o), radarToken(other.Key, o))
					continue
				}
				// This author has nothing to rebase: the pair no longer
				// conflicts, or this change merges first. Drop an undelivered
				// notice about the pair.
				for i := range r.Notices {
					n := &r.Notices[i]
					if n.Kind == "radar" && !n.Done && n.SupersededBy == "" && radarPairIs(n, *other) {
						n.SupersededBy = "radar-cleared"
					}
				}
			}
			sort.Slice(kept, func(i, j int) bool { return kept[i].ChangeID < kept[j].ChangeID })
			r.Overlaps = kept
		}
		return nil
	})
}

// radarToken is one pair notice identity: the other change and both heads.
func radarToken(otherKey string, o state.Overlap) string {
	return "radar:" + otherKey + ":" + o.Head + ":" + o.OtherHead
}

// radarPairIs reports whether a radar notice is about this pair.
// TODO: legacy/shim remove later — falls back to the change label in the text for notices saved before the pair identity was stored; remove when those notices have drained.
func radarPairIs(n *Notice, other Record) bool {
	if n.Other != "" {
		return n.Other == other.Key
	}
	return strings.Contains(n.Text, changeLabel(other)+" ")
}

// A PR based on another branch shares its whole stack's diff with main. The
// radar compares it after it is retargeted to the default branch.
func basedOnBranch(r Record) bool {
	return r.BaseRef != "" && r.DefaultBranch != "" && strings.TrimPrefix(r.BaseRef, "origin/") != r.DefaultBranch
}

func commitID(head string) bool {
	if len(head) != 40 && len(head) != 64 {
		return false
	}
	return strings.Trim(head, "0123456789abcdef") == ""
}

// Stacked changes overlap their parents on purpose.
func stacked(a, b Record) bool {
	dependsOn := func(r, base Record) bool {
		return slices.Contains(r.Dependencies, base.Key) || slices.Contains(r.Dependencies, changeID(base)) ||
			(base.Branch != "" && strings.TrimPrefix(r.BaseRef, "origin/") == base.Branch)
	}
	return dependsOn(a, b) || dependsOn(b, a)
}

func overlapWith(r Record, id string) (state.Overlap, bool) {
	for _, o := range r.Overlaps {
		if o.ChangeID == id {
			return o, true
		}
	}
	return state.Overlap{}, false
}

// aheadOf reports whether a merges before b. A queued change goes before an
// unqueued one, and two unqueued changes go by change ID, so exactly one
// author of a pair must rebase.
func aheadOf(a, b Record, order map[string]int) bool {
	i, aQueued := order[changeID(a)]
	j, bQueued := order[changeID(b)]
	if aQueued != bQueued {
		return aQueued
	}
	if aQueued {
		return i < j
	}
	return changeID(a) < changeID(b)
}

func conflictLine(ahead, behind Record, o state.Overlap) string {
	return fmt.Sprintf("%s and %s change the same lines in %s. %s is ahead in the merge queue. If it merges first, rebase %s and resolve these files.",
		changeLabel(behind), changeLabel(ahead), strings.Join(o.Conflicts, ", "), changeLabel(ahead), changeLabel(behind))
}

// overlapPaths returns the files both changes touch and the files where
// their lines conflict. A conflicting file is not also listed as shared.
// With a known main head, each PR's own change is measured from its merge
// base with main, so main's commits between two PR bases count for neither.
// The pair conflicts only when both merge into main cleanly on their own but
// not together. A PR that conflicts with main alone already has a rebase notice.
func (s *Service) overlapPaths(ctx context.Context, a, b Record, main string) (files, conflicts []string, err error) {
	repo := a.RepoPath
	if _, statErr := os.Stat(repo); statErr != nil {
		repo = b.RepoPath
	}
	heads := []Record{a, b}
	if main != "" {
		mainRecord := a
		mainRecord.Head = main
		heads = append(heads, mainRecord)
	}
	for _, r := range heads {
		if err := s.ensureCommit(ctx, repo, r); err != nil {
			return nil, nil, err
		}
	}
	changed := func(head string) ([]string, error) {
		other := b.Head
		if head == b.Head {
			other = a.Head
		}
		if main != "" {
			other = main
		}
		base, err := gitRead(ctx, repo, "merge-base", head, other)
		if err != nil {
			return nil, err
		}
		out, err := gitRead(ctx, repo, "diff", "--name-only", "--no-renames", "-z", base, head)
		return strings.FieldsFunc(out, func(c rune) bool { return c == 0 }), err
	}
	if main == "" {
		conflicts, err = mergeConflictPaths(ctx, repo, a.Head, b.Head)
	} else {
		conflicts, err = s.mainRelativeConflicts(ctx, repo, main, a.Head, b.Head)
	}
	if err != nil {
		return nil, nil, err
	}
	left, err := changed(a.Head)
	if err != nil {
		return nil, nil, err
	}
	right, err := changed(b.Head)
	if err != nil {
		return nil, nil, err
	}
	for _, path := range left {
		if slices.Contains(right, path) && !slices.Contains(conflicts, path) {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files, conflicts, nil
}

func (s *Service) mainRelativeConflicts(ctx context.Context, repo, main, a, b string) ([]string, error) {
	tree, alone, err := mergeTree(ctx, repo, main, a)
	if err != nil || len(alone) > 0 {
		return nil, err
	}
	if alone, err := mergeConflictPaths(ctx, repo, main, b); err != nil || len(alone) > 0 {
		return nil, err
	}
	// The merge commit stays a loose object; no ref points to it.
	merged, err := gitRead(ctx, repo, "-c", "user.name=Dyna radar", "-c", "user.email=radar@dyna.invalid", "commit-tree", tree, "-p", main, "-p", a, "-m", "radar")
	if err != nil {
		return nil, err
	}
	return mergeConflictPaths(ctx, repo, merged, b)
}

// ensureCommit fetches a PR head that the checkout lacks from its origin, by
// SHA, the same way the merge trial fetches frozen heads. It changes no branch.
func (s *Service) ensureCommit(ctx context.Context, repo string, r Record) error {
	if _, err := gitRead(ctx, repo, "cat-file", "-e", r.Head+"^{commit}"); err == nil {
		return nil
	}
	if changeKind(r) != "github" {
		return errors.New("change head " + r.Head + " is missing from " + repo)
	}
	cmd := ghclient.Command(ctx, "git", repo, "fetch", "--quiet", "--no-tags", "origin", r.Head)
	return s.trialClient(r).Run(ctx, cmd)
}
