package watch

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/worktrees"
)

// autoTrackInit protects lazy initialization of per-service discovery state.
var autoTrackInit sync.Mutex

// autoTrack consumes branch observations from the existing head batch.
// Track remains the one publication path, including its initial snapshot.
func (s *Service) autoTrack(ctx context.Context, st State, heads map[string]headProbe, owners worktreeOwners) {
	if s.Notify == nil {
		return
	}
	keys := make([]string, 0, len(heads))
	candidates := map[string][]discoveredPull{}
	for key, probe := range heads {
		r := st.Records[key]
		if r == nil {
			r = probe.Source
		}
		if r == nil {
			continue
		}
		ref := refRecord(*r)
		for _, pull := range probe.OpenPulls {
			ref.Number = pull.Number
			if pull.Number > 0 && pull.Branch != "" && pull.Branch != probe.Default.Name && !pull.Fork && headSHAPattern.MatchString(pull.Head) && recordByID(&st, ref.Key()) == nil {
				candidates[key] = append(candidates[key], pull)
			}
		}
		if len(candidates[key]) > 0 {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 || ctx.Err() != nil {
		return
	}
	autoTrackInit.Lock()
	if s.autoTrackFailures == nil {
		s.autoTrackFailures = &sync.Map{}
	}
	failures := s.autoTrackFailures
	autoTrackInit.Unlock()

	sort.Strings(keys)
	seen := map[string]bool{}
	for _, key := range keys {
		r := st.Records[key]
		if r == nil {
			r = heads[key].Source
		}
		if r == nil || r.RepoPath == "" {
			continue
		}
		ref := refRecord(*r)
		repoKey := strings.ToLower(ref.Host + "/" + ref.Repo)
		paused, err := s.RoomPaused(r.RoomID)
		if err != nil || paused {
			continue
		}
		rec, err := s.Notify.Rooms.Resolve(r.RoomID)
		if err != nil || rec.Repo == "" {
			continue
		}
		main := filepath.Clean(r.RepoPath)
		if main != filepath.Clean(rec.Repo) {
			continue
		}
		// Separate clones of one remote can belong to different rooms.
		ownerKey := repoKey + "\n" + main + "\n" + r.RoomID + "\n" + r.Branch
		if seen[ownerKey] {
			continue
		}
		seen[ownerKey] = true
		for _, pull := range candidates[key] {
			if ctx.Err() != nil {
				return
			}
			ref.Number = pull.Number
			if recordByID(&st, ref.Key()) != nil {
				continue
			}
			attempt := ref.Key() + "\n" + pull.Head
			if _, failed := failures.Load(attempt); failed {
				continue
			}
			author, path := s.worktreeAuthor(owners, main, r.RoomID, pull.Branch)
			if path == "" {
				continue
			}
			// Do not spend the sole attempt on an already exhausted poll budget.
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < s.recordBudget() {
				return
			}
			if _, claimed := failures.LoadOrStore(attempt, true); claimed {
				continue
			}
			trackCtx, stop := context.WithTimeout(ctx, s.recordBudget())
			tracked, _, err := s.trackWithSource(trackCtx, authority.Actor{Role: authority.Owner, Room: r.RoomID}, ref, author, path, PolicyOverride{}, author.ID, state.AuthorSourceDiscovery)
			stop()
			if err != nil {
				log.Printf("PR discovery track %s at %s: %s", ref.Key(), pull.Head, Sanitize(err.Error()))
				continue
			}
			st.Records[tracked.Key] = &tracked
			failures.Delete(attempt)
		}
	}
}

type worktreeOwnerKey struct{ main, room, branch string }
type worktreeOwner struct {
	author subagent.Info
	path   string
}
type worktreeOwners map[worktreeOwnerKey]worktreeOwner

// loadWorktreeOwners reads sessions once and indexes eligible branches once.
// Both query construction and tracking use this same ownership decision.
func (s *Service) loadWorktreeOwners(ctx context.Context, st State) worktreeOwners {
	owners := worktreeOwners{}
	if s.Notify == nil || ctx.Err() != nil || s.now().Before(st.LegacyPause) {
		return owners
	}
	trees, err := worktrees.List()
	if err != nil {
		log.Printf("PR discovery worktrees: %s", Sanitize(err.Error()))
		return owners
	}
	sessions, err := s.Notify.Sessions.Store.ListSnapshot(false)
	if err != nil {
		log.Printf("PR discovery sessions: %s", Sanitize(err.Error()))
		return owners
	}
	live := map[string]subagent.Info{}
	for _, info := range sessions {
		if info.Status == subagent.StatusIdle || info.Status == subagent.StatusRunning || info.Status == subagent.StatusQueued {
			live[info.ID] = info
		}
	}
	defaults := map[string]string{}
	for _, r := range st.Records {
		if r.RepoPath != "" {
			branch := st.RepositoryFacts[defaultRepo(*r)].DefaultBranch
			if branch == "" {
				branch = r.DefaultBranch
			}
			if branch != "" {
				defaults[filepath.Clean(r.RepoPath)] = branch
			}
		}
	}
	roomRepos := map[string]string{}
	for _, tree := range trees {
		if ctx.Err() != nil {
			break
		}
		main, path := filepath.Clean(tree.Repo), filepath.Clean(tree.Path)
		if tree.Session == "" || tree.Branch == "" || path == main {
			continue
		}
		info, ok := live[tree.Session]
		if !ok {
			continue
		}
		roomRepo, known := roomRepos[info.RoomID]
		if !known {
			paused, err := s.RoomPaused(info.RoomID)
			if err == nil && !paused {
				rec, err := s.Notify.Rooms.Resolve(info.RoomID)
				if err == nil && rec.Repo != "" {
					roomRepo = filepath.Clean(rec.Repo)
				}
			}
			roomRepos[info.RoomID] = roomRepo
		}
		if roomRepo != main {
			continue
		}
		branch, known := defaults[main]
		if !known {
			// Resolve the local default when this repo has no saved branch fact.
			resolved, _ := repoid.DefaultBranch(ctx, main, gitRead)
			branch = resolved.Name
			defaults[main] = branch
		}
		if tree.Branch == branch {
			continue
		}
		if dir, err := os.Stat(path); err != nil || !dir.IsDir() {
			continue
		}
		key := worktreeOwnerKey{main, info.RoomID, tree.Branch}
		if _, exists := owners[key]; exists {
			owners[key] = worktreeOwner{} // Ambiguous ownership cannot assign an author.
			continue
		}
		owners[key] = worktreeOwner{author: info, path: path}
	}
	return owners
}

func (s *Service) worktreeAuthor(owners worktreeOwners, main, roomID, branch string) (subagent.Info, string) {
	owner := owners[worktreeOwnerKey{main, roomID, branch}]
	return owner.author, owner.path
}

// discoveryRecords supplies transient query inputs, never saved PR records.
func (s *Service) discoveryRecords(ctx context.Context, st State, owners worktreeOwners) []*Record {
	if s.Notify == nil || s.now().Before(st.LegacyPause) {
		return nil
	}
	var records []*Record
	remotes := map[string]Ref{}
	keys := make([]worktreeOwnerKey, 0, len(owners))
	for key, owner := range owners {
		if owner.path != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.main != b.main {
			return a.main < b.main
		}
		if a.room != b.room {
			return a.room < b.room
		}
		return a.branch < b.branch
	})
	trackedBranches := map[string]bool{}
	for _, r := range st.Records {
		if r.Watching && r.State == state.PROpen {
			trackedBranches[strings.ToLower(r.Host+"/"+r.Repo)+"\n"+r.Branch] = true
		}
	}
	for _, ownerKey := range keys {
		if ctx.Err() != nil {
			break
		}
		main := ownerKey.main
		key := "discovery:" + ownerKey.room + ":" + ownerKey.branch
		ref, known := remotes[main]
		if !known {
			// Existing tracked identity avoids Git reads for known clones.
			for _, r := range st.Records {
				if r.RepoPath == main && changeKind(*r) != "local" {
					ref = refRecord(*r)
					break
				}
			}
			if ref.Host == "" {
				remote, err := gitRead(ctx, main, "remote", "get-url", "origin")
				if err != nil {
					continue
				}
				id, err := repoid.RemoteIdentity(main, remote)
				if err != nil {
					continue
				}
				host, repo, ok := strings.Cut(id, "/")
				if !ok || strings.Count(repo, "/") != 1 || host == "" {
					continue
				}
				ref = Ref{Host: host, Repo: repo}
			}
			remotes[main] = ref
		}
		if !trackedBranches[strings.ToLower(ref.Host+"/"+ref.Repo)+"\n"+ownerKey.branch] {
			records = append(records, &Record{Key: key, Host: ref.Host, Repo: ref.Repo, RepoPath: main, RoomID: ownerKey.room, Branch: ownerKey.branch})
		}
	}
	return records
}
