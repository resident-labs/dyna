package watch

import (
	"context"
	"sort"
)

type commitRollup struct {
	State    string `json:"state"`
	Contexts struct {
		TotalCount int                `json:"totalCount"`
		CheckRuns  []rollupStateCount `json:"checkRunCountsByState"`
		Statuses   []rollupStateCount `json:"statusContextCountsByState"`
	} `json:"contexts"`
}

type rollupStateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// GitHub can report FAILURE while a replacement for a cancelled run is active.
// State counts cover every context without the pagination limit of nodes.
func (r *commitRollup) pending() bool {
	for _, run := range r.Contexts.CheckRuns {
		if run.Count == 0 {
			continue
		}
		switch run.State {
		case "ACTION_REQUIRED", "CANCELLED", "COMPLETED", "FAILURE", "NEUTRAL", "SKIPPED", "STALE", "STARTUP_FAILURE", "SUCCESS", "TIMED_OUT":
		default:
			return true
		}
	}
	for _, status := range r.Contexts.Statuses {
		if status.Count == 0 {
			continue
		}
		switch status.State {
		case "ERROR", "FAILURE", "SUCCESS":
		default:
			return true
		}
	}
	return false
}

type defaultCommit struct {
	Head   string        `json:"oid"`
	Rollup *commitRollup `json:"statusCheckRollup"`
}

// cancelledChecks reports a rollup that counted cancelled check runs.
func (r *commitRollup) cancelledChecks() bool {
	for _, check := range r.Contexts.CheckRuns {
		if check.Count > 0 && check.State == "CANCELLED" {
			return true
		}
	}
	return false
}

// cancelledFailure reports a red rollup that counted a cancelled run.
func (p defaultBranchProbe) cancelledFailure() bool {
	return p.CI == "FAILURE" && p.Rollup != nil && p.Rollup.cancelledChecks()
}

func defaultHeadConfirmed(p defaultBranchProbe) bool {
	return p.Name != "" && (len(p.Head) == 40 || len(p.Head) == 64) && headSHAPattern.MatchString(p.Head)
}

func defaultVerification(p defaultBranchProbe) string {
	if !defaultHeadConfirmed(p) || p.ChecksPending {
		return "pending"
	}
	if p.NoPushCI && p.CheckCount == 0 {
		return "passed"
	}
	if p.CheckCount > 0 {
		switch p.CI {
		case "SUCCESS":
			return "passed"
		case "FAILURE", "ERROR":
			return "failed"
		}
	}
	return "pending"
}

func defaultRepo(r Record) string {
	if changeKind(r) == "local" {
		return r.RepoPath
	}
	return r.Host + "/" + r.Repo
}

// Missing observations remain pending until the shared writer confirms them.
func storedDefault(st State, r Record) (defaultBranchProbe, string) {
	f := st.RepositoryFacts[defaultRepo(r)]
	p := defaultBranchProbe{Name: f.DefaultBranch, Head: f.Head, NoPushCI: f.NoPushCI}
	if f.CheckedAt.IsZero() {
		return p, "pending"
	}
	switch f.Verdict {
	case "passed", "failed":
		return p, f.Verdict
	default:
		return p, "pending"
	}
}

// Default branches are repository facts. One representative per unpaused
// repository keeps idle repositories covered without saving merge state.
func repositoryRepresentatives(st State, paused func(string) (bool, error)) ([]Record, error) {
	keys := make([]string, 0, len(st.Records))
	for key := range st.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	active := map[string]map[string]Record{}
	latest := map[string]Record{}
	for _, key := range keys {
		r := st.Records[key]
		if changeKind(*r) != "github" || r.Number < 1 {
			continue
		}
		repo := defaultRepo(*r)
		if !r.Watching || r.State != "open" {
			if r.State == "merged" {
				old, found := latest[repo]
				if !found || r.ClosedAt.After(old.ClosedAt) {
					latest[repo] = *r
				}
			}
			continue
		}
		if active[repo] == nil {
			active[repo] = map[string]Record{}
		}
		if _, found := active[repo][r.RoomID]; !found {
			active[repo][r.RoomID] = *r
		}
	}
	for repo, r := range latest {
		if active[repo] == nil {
			active[repo] = map[string]Record{r.RoomID: r}
		}
	}
	byRoom := map[string][]Record{}
	for _, rooms := range active {
		for room, r := range rooms {
			byRoom[room] = append(byRoom[room], r)
		}
	}
	rooms := make([]string, 0, len(byRoom))
	for room := range byRoom {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	byRepo := map[string]Record{}
	for _, room := range rooms {
		isPaused, err := paused(room)
		if err != nil {
			return nil, err
		}
		if isPaused {
			continue
		}
		for _, r := range byRoom[room] {
			repo := defaultRepo(r)
			if _, found := byRepo[repo]; !found {
				byRepo[repo] = r
			}
		}
	}
	repos := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	result := make([]Record, 0, len(repos))
	for _, repo := range repos {
		result = append(result, byRepo[repo])
	}
	return result, nil
}

func (s *Service) refreshRepositoryFacts(ctx context.Context, st State) ([]Record, error) {
	pauses := map[string]bool{}
	selected, err := repositoryRepresentatives(st, func(room string) (bool, error) { return s.roomPausedCached(room, pauses) })
	if err != nil {
		return nil, err
	}
	inputs := make([]*Record, 0, len(selected))
	for i := range selected {
		if !selected[i].Watching || selected[i].State != "open" {
			inputs = append(inputs, &selected[i])
		}
	}
	if len(inputs) == 0 {
		return selected, nil
	}
	_, err = s.observeHeadBatch(ctx, st, inputs, nil)
	return selected, err
}

// defaultWait names why the queue waits on the default branch, as facts. The
// web words the kind and shortens MainCI.Head, so no sentence or full commit
// reaches the screen from here.
func defaultWait(p defaultBranchProbe, status string) *MainWait {
	switch {
	case p.NoPushCI:
		return &MainWait{Kind: MainWaitNoPushCI}
	case p.Name == "" || p.Head == "":
		return &MainWait{Kind: MainWaitNoHead}
	case status == "passed":
		return &MainWait{Kind: MainWaitPassed, Base: p.Name}
	case status == "failed":
		return &MainWait{Kind: MainWaitFailed, Base: p.Name}
	}
	return &MainWait{Kind: MainWaitPending, Base: p.Name}
}

func defaultQueueState(status string) string {
	switch status {
	case "passed":
		return "main-passed"
	case "failed":
		return "main-failed"
	default:
		return "main-pending"
	}
}
