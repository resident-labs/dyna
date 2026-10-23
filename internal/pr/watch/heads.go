package watch

import (
	"context"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/facts"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type defaultBranchProbe struct {
	Name, Head    string
	CI            string
	CheckCount    int
	ChecksPending bool
	NoPushCI      bool
	MergeTrialCI  bool
	// Rollup is GitHub's own status summary. A red rollup that counted a
	// cancelled run is re-read from the commit, because GitHub keeps counting a
	// cancellation that a later run of the same workflow superseded.
	Rollup *commitRollup
}

type discoveredPull struct {
	Number int    `json:"number"`
	Branch string `json:"headRefName"`
	Fork   bool   `json:"isCrossRepository"`
	Head   string `json:"headRefOid"`
}

type headProbe struct {
	// Discovery stays in this observation and is never saved to PR state.
	OpenPulls []discoveredPull
	Source    *Record // In-memory worktree context for an untracked branch.
	Default   defaultBranchProbe
	Account   string
	deferred  bool // A known pause retains identity without replacing head facts.
	Pull      Pull
	CI        string
	IsDraft   bool
	Mergeable *bool
	ChecksAt  time.Time
	Err       error
}

// ciVersion includes the latest check completion or status creation. A rerun
// can leave the rollup green while replacing the actual executed checks.
func (p headProbe) ciVersion() string {
	if p.CI == "" || p.ChecksAt.IsZero() {
		return p.CI
	}
	return p.CI + ":" + p.ChecksAt.UTC().Format(time.RFC3339Nano)
}

// readHeads confirms all tracked heads before any per-PR snapshot requests.
func (g GitHub) readHeads(ctx context.Context, records []*Record) (map[string]headProbe, map[string]RateLimit, error) {
	if g.Rooms != nil {
		ctx = ghclient.WithRooms(ctx, g.Rooms)
	}
	type accountGroup struct{ host, login string }
	byAccount := map[accountGroup]map[string][]*Record{}
	selected := map[string]string{}
	out := map[string]headProbe{}
	var failures []error
	for _, r := range records {
		host := strings.ToLower(r.Host)
		key := host + "/" + r.Repo + "\n" + r.RoomID
		login, found := selected[key]
		if !found && g.run == nil {
			var err error
			login, err = ghclient.AccountFor(ghclient.WithRoom(ctx, r.RoomID), g.Executable, host, r.Repo)
			if err != nil {
				out[r.Key] = headProbe{Err: err}
				failures = append(failures, err)
				continue
			}
			selected[key] = login
		}
		group := accountGroup{host, login}
		if byAccount[group] == nil {
			byAccount[group] = map[string][]*Record{}
		}
		byAccount[group][r.Repo] = append(byAccount[group][r.Repo], r)
	}
	groups := []accountGroup{}
	for group := range byAccount {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].host != groups[j].host {
			return groups[i].host < groups[j].host
		}
		return groups[i].login < groups[j].login
	})
	rates := map[string]RateLimit{}
	if g.requests != nil {
		rates = g.requests.rates
	}
	now := g.now
	if g.requests != nil && g.requests.now != nil {
		now = g.requests.now
	}
	if now == nil {
		now = time.Now
	}
	if g.requests == nil {
		g.requests = &requestCache{rates: rates, now: now}
	}
	for _, group := range groups {
		if until := g.requests.pauseUntil(RateLimitScope{Host: group.host, Account: group.login, Resource: "graphql"}); now().Before(until) {
			for _, rs := range byAccount[group] {
				for _, r := range rs {
					out[r.Key] = headProbe{Account: group.login, Err: &rateLimitError{until}, deferred: true}
				}
			}
			continue
		}
		host := group.host
		grouped := byAccount[group]
		repos := []string{}
		for repo := range grouped {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		var query strings.Builder
		query.WriteString("query {")
		for i, repo := range repos {
			owner, name, ok := strings.Cut(repo, "/")
			if !ok {
				return nil, rates, fmt.Errorf("invalid repository %q", repo)
			}

			fmt.Fprintf(&query, "r%d:repository(owner:%s,name:%s){defaultBranchRef{name target{... on Commit{oid statusCheckRollup{state contexts{totalCount checkRunCountsByState{state count} statusContextCountsByState{state count}}} workflows:file(path:\".github/workflows\"){object{... on Tree{entries{name type object{... on Blob{text isTruncated}}}}}}}}}", i, strconv.Quote(owner), strconv.Quote(name))
			sort.Slice(grouped[repo], func(a, b int) bool {
				x, y := grouped[repo][a], grouped[repo][b]
				if x.Number != y.Number {
					return x.Number < y.Number
				}
				return x.Key < y.Key
			})
			for j, r := range grouped[repo] {
				if r.Number == 0 {
					fmt.Fprintf(&query, "b%d:pullRequests(first:1,headRefName:%s,states:OPEN){nodes{number headRefName isCrossRepository headRefOid}}", j, strconv.Quote(r.Branch))
					continue
				}
				fmt.Fprintf(&query, "p%d:pullRequest(number:%d){number title state merged isDraft mergeable updatedAt headRefOid headRefName headRepository{nameWithOwner} baseRefOid baseRefName author{login __typename} commits(last:1){nodes{commit{statusCheckRollup{state contexts(first:100){nodes{... on CheckRun{completedAt} ... on StatusContext{createdAt}}}}}}}}", r.Number, r.Number)
			}
			query.WriteString("}")
		}
		query.WriteString("}")
		g.requests.account = group.login
		body, _, callErr := g.requestMethod(ghclient.WithRoom(ctx, grouped[repos[0]][0].RoomID), Ref{Host: host, Repo: repos[0]}, "graphql", "POST", "-f", "query="+query.String())
		var response struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		decodeErr := json.Unmarshal(body, &response)
		if decodeErr != nil || len(response.Data) == 0 {
			err := callErr
			if err == nil {
				err = decodeErr
			}
			if err == nil {
				err = fmt.Errorf("GitHub returned no head metadata")
			}
			failures = append(failures, err)
			for _, rs := range grouped {
				for _, r := range rs {
					out[r.Key] = headProbe{Account: g.requests.account, Err: err}
				}
			}
			continue
		}
		for i, repo := range repos {
			var nodes map[string]json.RawMessage
			_ = json.Unmarshal(response.Data[fmt.Sprintf("r%d", i)], &nodes)
			var defaultBranch struct {
				Name   string `json:"name"`
				Target struct {
					defaultCommit
					Workflows json.RawMessage `json:"workflows"`
				} `json:"target"`
			}
			if raw := nodes["defaultBranchRef"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &defaultBranch); err != nil {
					return nil, rates, err
				}
			}
			branch := defaultBranchProbe{Name: defaultBranch.Name, Head: defaultBranch.Target.Head}
			if callErr == nil && len(defaultBranch.Target.Workflows) > 0 {
				branch.NoPushCI = noDefaultPushCI(defaultBranch.Target.Workflows, branch.Name)
				branch.MergeTrialCI, _ = workflowPushEnabled(defaultBranch.Target.Workflows, "dyna/merge-trial/1")
			}
			if rollup := defaultBranch.Target.Rollup; rollup != nil {
				branch.CI, branch.CheckCount = rollup.State, rollup.Contexts.TotalCount
				branch.ChecksPending = rollup.pending()
				branch.Rollup = rollup
			}
			branch.NoPushCI = branch.NoPushCI && branch.CheckCount == 0

			for j, r := range grouped[repo] {
				if r.Number == 0 {
					var open struct {
						Nodes []discoveredPull `json:"nodes"`
					}
					if err := json.Unmarshal(nodes[fmt.Sprintf("b%d", j)], &open); err == nil {
						out[r.Key] = headProbe{Source: r, OpenPulls: open.Nodes, Default: branch}
					}
					continue
				}
				var node struct {
					IsDraft        bool   `json:"isDraft"`
					Mergeable      string `json:"mergeable"`
					Number         int    `json:"number"`
					Title          string `json:"title"`
					State          string `json:"state"`
					Merged         bool   `json:"merged"`
					Updated        string `json:"updatedAt"`
					Head           string `json:"headRefOid"`
					HeadRepository struct {
						Name string `json:"nameWithOwner"`
					} `json:"headRepository"`
					Branch  string `json:"headRefName"`
					Base    string `json:"baseRefOid"`
					BaseRef string `json:"baseRefName"`
					Author  struct {
						Login string `json:"login"`
						Type  string `json:"__typename"`
					} `json:"author"`
					Commits struct {
						Nodes []struct {
							Commit struct {
								Rollup *struct {
									State    string `json:"state"`
									Contexts struct {
										Nodes []struct {
											CompletedAt time.Time `json:"completedAt"`
											CreatedAt   time.Time `json:"createdAt"`
										} `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				}
				err := json.Unmarshal(nodes[fmt.Sprintf("p%d", r.Number)], &node)
				if err == nil && (node.Number != r.Number || !headSHAPattern.MatchString(node.Head) || (node.State != "OPEN" && node.State != "CLOSED" && node.State != "MERGED")) {
					err = fmt.Errorf("GitHub returned invalid head metadata for %s", r.Key)
				}
				if err != nil {
					out[r.Key] = headProbe{Err: err}
					failures = append(failures, err)
					continue
				}
				p := Pull{Number: node.Number, Title: node.Title, Draft: node.IsDraft, Updated: node.Updated, State: strings.ToLower(node.State), Merged: node.Merged, User: User{Login: node.Author.Login, Type: node.Author.Type}}
				if node.State == "MERGED" {
					p.State = "closed"
					p.Merged = true
				}
				p.Head.SHA, p.Head.Ref = node.Head, node.Branch
				p.Head.Repo.FullName = node.HeadRepository.Name
				p.Base.SHA, p.Base.Ref, p.Base.Repo.DefaultBranch = node.Base, node.BaseRef, defaultBranch.Name
				ci := ""
				var checksAt time.Time
				var mergeable *bool
				if node.Mergeable == "MERGEABLE" || node.Mergeable == "CONFLICTING" {
					b := node.Mergeable == "MERGEABLE"
					mergeable = &b
				}
				if len(node.Commits.Nodes) > 0 && node.Commits.Nodes[0].Commit.Rollup != nil {
					rollup := node.Commits.Nodes[0].Commit.Rollup
					ci = rollup.State
					for _, check := range rollup.Contexts.Nodes {
						if check.CompletedAt.After(checksAt) {
							checksAt = check.CompletedAt
						}
						if check.CreatedAt.After(checksAt) {
							checksAt = check.CreatedAt
						}
					}
				}
				out[r.Key] = headProbe{Account: g.requests.account, Default: branch, Pull: p, CI: ci, IsDraft: node.IsDraft, Mergeable: mergeable, ChecksAt: checksAt}
			}
		}
	}
	return out, rates, errors.Join(failures...)
}

func (s *Service) refreshHeads(ctx context.Context, st State, polledAt time.Time, pauses map[string]bool, discovery ...*Record) (map[string]headProbe, error) {
	if s.now().Before(st.LegacyPause) {
		return map[string]headProbe{}, nil
	}
	records := append([]*Record{}, discovery...)
	resumedAt := map[string]time.Time{}
	if pauses == nil {
		pauses = map[string]bool{}
	}
	for _, r := range st.Records {
		if !r.Watching || changeKind(*r) == "local" {
			continue
		}
		paused, err := s.roomPausedCached(r.RoomID, pauses)
		if err != nil {
			return nil, err
		}
		if !paused {
			if s.Notify != nil && s.Notify.Rooms != nil {
				if _, known := resumedAt[r.RoomID]; !known {
					state, err := s.Notify.Rooms.SessionPause(r.RoomID)
					if err != nil {
						return nil, err
					}
					if state.Paused {
						pauses[r.RoomID] = true
						continue
					}
					resumedAt[r.RoomID] = state.ResumedAt
				}
			}
			records = append(records, r)
		}
	}
	if len(records) == 0 {
		return map[string]headProbe{}, nil
	}
	return s.observeHeadBatch(ctx, st, records, func(current *State, heads map[string]headProbe) error {
		for key, probe := range heads {
			r := current.Records[key]
			if r == nil || !r.Watching || probe.deferred {
				continue
			}
			if probe.Err != nil {
				r.HeadStale = true
				r.Error = Sanitize(probe.Err.Error())
				r.Polling.Next = nextPollTime(polledAt, 1)
				continue
			}
			pull := probe.Pull
			// A confirmed head keeps its first confirmation time. Stamping it
			// every minute would rewrite every watched record on every tick.
			if r.Head != pull.Head.SHA || !headVerified(*r) || r.HeadVerifiedAt.IsZero() {
				r.HeadVerifiedAt = s.now()
			}
			if r.Polling.PullVersion != pullVersion(pull, EffectivePolicy(*current, *r)) {
				for path := range current.Responses[key] {
					if strings.Contains(path, "/comments?") || strings.Contains(path, "/reviews?") {
						delete(current.Responses[key], path)
					}
				}
			}
			if r.Head != pull.Head.SHA {
				if r.Polling.PullHead == "" {
					r.Polling.PullHead = r.Head
				}
				resetReview(r, pull.Head.SHA)
				r.HeadSeen = s.now()
				r.Polling.Next = time.Time{}
			}
			if r.Polling.CIVersion != probe.ciVersion() {
				r.CI = "pending"
				if probe.CI == "FAILURE" || probe.CI == "ERROR" {
					r.CI = "failed"
				}
				r.Polling.Next = time.Time{}
			}
			// A ready transition must take a full observation now. Reusing CI
			// must not preserve the draft's longer safety schedule.
			if r.IsDraft != probe.IsDraft {
				r.Polling.FullChecked = time.Time{}
				r.Polling.Next = time.Time{}
			}
			r.HeadRepo = pull.Head.Repo.FullName
			r.IsDraft, r.Mergeable, r.DefaultBranch, r.ChecksAt = probe.IsDraft, probe.Mergeable, pull.Base.Repo.DefaultBranch, probe.ChecksAt
			r.Title = Sanitize(pull.Title)
			r.HeadStale, r.Error = false, ""
			// A room resume requires one full observation even if an earlier
			// outage or reserve schedule had postponed the next poll.
			if resumedAt[r.RoomID].After(r.Polling.FullChecked) {
				r.Polling.Next = time.Time{}
				r.Polling.FullChecked = time.Time{}
			}
			// The normal snapshot path still owns end notices, hooks and cleanup.
			// Mark the known state now so closed PRs cannot acquire a new review.
			if pull.State == "closed" {
				endRecord(r, pull.Merged, s.now())
				r.Polling.Next = time.Time{}
			}
			observation := facts.FromRecord(*r)
			observation.Head, observation.Base = pull.Head.SHA, pull.Base.Ref
			noticeIfLandable(*current, r, observation)
		}
		return nil
	})
}

// All batched observations share rate backoff and one atomic store update.
func (s *Service) observeHeadBatch(ctx context.Context, st State, records []*Record, save func(*State, map[string]headProbe) error) (map[string]headProbe, error) {
	// GitHub can refresh PR heads for review while the local lane owns merge facts.
	localRepos := map[string]bool{}
	for _, r := range records {
		local, err := recordLocalLane(ctx, *r)
		if err != nil {
			return nil, err
		}
		localRepos[defaultRepo(*r)] = localRepos[defaultRepo(*r)] || local
	}
	observedAt := s.now()
	if observedAt.Before(st.LegacyPause) {
		return map[string]headProbe{}, nil
	}
	gh := s.GH
	gh.requests = &requestCache{rates: st.RateLimits, legacyPause: st.LegacyPause, now: s.now}
	heads, rates, readErr := gh.readHeads(ctx, records)
	recheckErr := s.recheckCancelledMainCI(ctx, gh, records, localRepos, heads)
	saveErr := s.Store.update(func(current *State) error {
		for key, probe := range heads {
			if r := current.Records[key]; r != nil && probe.Account != "" {
				r.QuotaAccount = probe.Account
			}
		}
		if current.RepositoryFacts == nil {
			current.RepositoryFacts = map[string]RepositoryFacts{}
		}
		for _, r := range records {
			if localRepos[defaultRepo(*r)] {
				continue
			}
			if probe, ok := heads[r.Key]; ok && probe.Err == nil {
				// Response completion order must not make an older green query
				// replace failed-main evidence from a newer query.
				if current.RepositoryFacts[defaultRepo(*r)].CheckedAt.After(observedAt) {
					continue
				}
				current.RepositoryFacts[defaultRepo(*r)] = RepositoryFacts{
					DefaultBranch: probe.Default.Name, Head: probe.Default.Head,
					Verdict: defaultVerification(probe.Default), CheckedAt: observedAt, NoPushCI: probe.Default.NoPushCI, MergeTrialCI: probe.Default.MergeTrialCI,
				}
			}
		}
		if save != nil {
			if err := save(current, heads); err != nil {
				return err
			}
		}
		applyRateLimits(current, rates, s.now())
		return nil
	})
	return heads, errors.Join(readErr, recheckErr, saveErr)
}

// recheckCancelledMainCI replaces a red rollup that counted a cancelled run with
// the commit's own CI. GitHub keeps counting a cancelled run after a later run of
// the same workflow passed, so the latest completed result of each check decides
// and a superseded cancellation does not fail main. One read serves every PR of
// the repository, and every probe of that head gets the same result.
func (s *Service) recheckCancelledMainCI(ctx context.Context, gh GitHub, records []*Record, localRepos map[string]bool, heads map[string]headProbe) error {
	results := map[string]mainCIResult{}
	var failures []error
	for _, r := range records {
		probe, ok := heads[r.Key]
		if !ok || probe.Err != nil || localRepos[defaultRepo(*r)] || !probe.Default.cancelledFailure() {
			continue
		}
		identity := defaultRepo(*r) + "@" + probe.Default.Head
		result, read := results[identity]
		if !read {
			var snap Snapshot
			if err := gh.readCommitCI(ctx, refRecord(*r), probe.Default.Head, &snap); err != nil {
				failures = append(failures, err)
				continue
			}
			snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = probe.Default.Head, r.Repo
			result = mainCIResult{state: rollupState(mainCIVerdict(snap)), count: len(snap.currentChecks())}
			results[identity] = result
		}
		probe.Default.CI, probe.Default.CheckCount = result.state, result.count
		probe.Default.ChecksPending = result.state == "PENDING"
		heads[r.Key] = probe
	}
	return errors.Join(failures...)
}

// mainCIResult is the rechecked CI of one commit, shared by every probe of it.
type mainCIResult struct {
	state string
	count int
}

// mainCIVerdict judges every context of a commit the way the default-branch
// rollup does, so a recheck never ignores an external check or a commit status
// that the rollup counted: the latest result of each check and status decides,
// and a cancelled context that a later result replaced does not count.
func mainCIVerdict(snap Snapshot) string {
	failed, pending, passed := false, false, false
	for _, check := range snap.currentChecks() {
		switch checkState(check.Status, check.Conclusion) {
		case "failure", "cancelled":
			// A cancelled check that no later result replaced never tested
			// the commit, and CI cannot pass until it runs again.
			failed = true
		case "pending":
			pending = true
		case "success":
			passed = true
		}
	}
	latest := map[string]Status{}
	for _, v := range snap.Statuses {
		if localCommitStatus(v) {
			continue
		}
		if old, ok := latest[v.Context]; !ok || v.ID > old.ID {
			latest[v.Context] = v
		}
	}
	for _, v := range latest {
		switch v.State {
		case "failure", "error":
			failed = true
		case "success":
			passed = true
		default:
			pending = true
		}
	}
	switch {
	case failed:
		return "failed"
	case pending:
		return "pending"
	case passed:
		return "passed"
	}
	return "pending"
}

// rollupState spells a computed CI state the way the default-branch verdict reads it.
func rollupState(ci string) string {
	switch ci {
	case "passed":
		return "SUCCESS"
	case "failed":
		return "FAILURE"
	}
	return "PENDING"
}

// ConfirmHeads refreshes remote facts through their single batched writer.
// With no keys it confirms all watched PRs; keys limit an explicit action.
// Callers must stop the action on error and then read the confirmed records.
func (s *Service) ConfirmHeads(ctx context.Context, keys ...string) error {
	_, err := s.confirmedHeads(ctx, keys...)
	return err
}

func (s *Service) confirmedHeads(ctx context.Context, keys ...string) (map[string]headProbe, error) {
	st, err := s.Store.readRecords(keys...)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if recordByID(&st, key) == nil {
			return nil, fmt.Errorf("change %s is not tracked", key)
		}
	}
	refreshCtx, stop := context.WithTimeout(ctx, s.recordBudget())
	defer stop()
	probes, err := s.refreshHeads(refreshCtx, st, s.now(), nil)
	if err != nil {
		return nil, err
	}
	for key, r := range st.Records {
		if r.Watching && changeKind(*r) != "local" {
			paused, err := s.RoomPaused(r.RoomID)
			if err != nil {
				return nil, err
			}
			if paused {
				if len(keys) > 0 {
					return nil, fmt.Errorf("room sessions are paused")
				}
				continue
			}
			if probe, ok := probes[key]; !ok || probe.Err != nil {
				return nil, fmt.Errorf("GitHub head could not be confirmed; wait for a successful refresh")
			}
		}
	}
	return probes, nil
}
