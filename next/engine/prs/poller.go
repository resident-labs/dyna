package prs

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	// PollEvery is how often the engine runs a pass. It is flat, because an unchanged feed costs no quota.
	PollEvery = time.Minute
	// lowQuotaWait is the shortest gap between reads of a repository with under 20% of the quota left.
	lowQuotaWait = 10 * time.Minute
	// ciGrace is how long after a new head a PR with no checks stays read, because GitHub adds them shortly after a push and no feed says so.
	ciGrace = 10 * time.Minute
)

// Poller reads the feeds of each repository a room lists or a tracked PR or issue sits on (spec 7.7).
type Poller struct {
	Store    *store.Store
	GitHub   *Client
	Accounts *Accounts
	Now      func() time.Time

	last   map[contract.RepoName]time.Time // when each repository was last read
	failed map[contract.RepoName]string    // the last error logged for each, so a repository that stays broken logs once
}

// Run runs a pass on each tick until ctx ends.
func (p *Poller) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := p.Pass(ctx); err != nil && ctx.Err() == nil {
				log.Printf("github pass: %v", err)
			}
		}
	}
}

// due lists the repositories to read now. That is every one of them, until the quota runs low.
func (p *Poller) due(ctx context.Context, repos map[contract.RepoName]bool) []contract.RepoName {
	if p.last == nil {
		p.last, p.failed = map[contract.RepoName]time.Time{}, map[contract.RepoName]string{}
	}
	now := p.Now()
	low := false
	if p.Accounts == nil {
		_, low = p.GitHub.Low()
	}
	var due []contract.RepoName
	for repo := range repos {
		if p.Accounts != nil {
			client, err := p.Accounts.Repo(ctx, repo)
			if err == nil {
				_, low = client.Low()
			} else {
				low = false
			}
		}
		if last, read := p.last[repo]; !low || !read || !now.Before(last.Add(lowQuotaWait)) {
			due = append(due, repo)
			p.last[repo] = now
		}
	}
	slices.Sort(due)
	return due
}

// needsRead is a PR no feed will speak for: GitHub is still running its checks or working out whether it merges, and a feed
// shows neither ending.
func needsRead(row store.TrackedPR, now time.Time) bool {
	waitingForChecks := row.CI == contract.PrCINone && now.Before(time.UnixMilli(row.Meta.HeadAt).Add(ciGrace))
	return row.CI == contract.PrCIUnread || row.CI == contract.PrCIPending || waitingForChecks || row.Meta.Recheck
}

func reopens(items []contract.GitHubItem) bool {
	return slices.ContainsFunc(items, func(item contract.GitHubItem) bool { return item.Action == contract.GitHubReopened })
}

// repository reads one repository's feeds and works out what they mean. A failure anywhere keeps its cursors unmoved, so the
// next pass reads the same news again.
func (p *Poller) repository(ctx context.Context, repo contract.RepoName, pulls []store.TrackedPR, issues []store.TrackedIssue, heads map[contract.RepoName]store.RepoState, feeds map[string]store.GitHubFeed) (store.GitHubPass, error) {
	var pass store.GitHubPass
	client := p.GitHub
	if p.Accounts != nil {
		var err error
		client, err = p.Accounts.Repo(ctx, repo)
		if err != nil {
			return pass, err
		}
		if err := client.LoadViewer(ctx); err != nil {
			return pass, err
		}
	}
	probe, err := client.Probe(ctx, repo, feeds)
	if err != nil {
		return pass, err
	}
	now, viewer := p.Now(), client.Viewer()
	items := map[int][]contract.GitHubItem{}
	for _, item := range probe.Items {
		items[item.Number] = append(items[item.Number], item)
	}
	// A closed item is read no more, unless a feed reopens it. What else happens on it is not its owner's to hear.
	watched := func(open bool, number int) bool { return open || reopens(items[number]) }
	var numbers []int
	for _, row := range pulls {
		if watched(row.State == contract.PrOpen, row.Number) && (probe.Seen[row.Number] != nil || len(items[row.Number]) > 0 || probe.Moved || probe.Resync || row.Baseline || needsRead(row, now)) {
			numbers = append(numbers, row.Number)
		}
	}
	observed, err := client.Read(ctx, repo, numbers)
	if err != nil {
		return pass, err
	}
	for _, row := range pulls {
		if !watched(row.State == contract.PrOpen, row.Number) {
			continue
		}
		if update, changed := ComparePR(row, items[row.Number], probe.Seen[row.Number], observed[row.Number], viewer, now); changed {
			pass.PRs = append(pass.PRs, update)
		}
		delete(items, row.Number)
	}
	for _, row := range issues {
		if !watched(row.State == contract.IssueOpen, row.Number) {
			continue
		}
		listed, state := probe.Seen[row.Number], contract.IssueState("")
		// Issues have no query in a usual pass, so one the feeds lost is read on its own.
		if probe.Resync {
			_, item, err := client.Issue(ctx, repo, row.Number)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return pass, err
			}
			if err == nil {
				listed, state = &item, contract.IssueState(item.State)
			}
		}
		if update, changed := CompareIssue(row, items[row.Number], listed, state, viewer, now); changed {
			pass.Issues = append(pass.Issues, update)
		}
		delete(items, row.Number)
	}
	for _, item := range probe.Items {
		_, untracked := items[item.Number]
		if body := item.Event(); untracked || body != nil && item.Action != contract.GitHubOpened && item.Action != contract.GitHubMerged && item.Action != contract.GitHubClosed {
			pass.Items = append(pass.Items, item)
		}
	}
	stored, listed := heads[repo]
	if listed && probe.Main != "" && probe.Main != stored.Head {
		pass.Heads = append(pass.Heads, store.RepoHead{Repo: repo, Head: probe.Main})
	}
	// The CI of every head of the recent list that is not settled, in one query with an alias for each (spec 7.7). A repository whose first head is not stored yet has none to read.
	unsettled := stored.Unsettled
	if probe.Main != "" && probe.Main != stored.Head {
		// The write that adds the new head prunes the list to its newest store.HeadsKept, so the oldest head past that is not read.
		unsettled = append(slices.Clone(unsettled), store.ListedHead{Sha: probe.Main, SeenAt: now})
		unsettled = unsettled[max(0, len(unsettled)-store.HeadsKept):]
	}
	if listed && stored.Head != "" && probe.Branch != "" && len(unsettled) > 0 {
		shas := make([]string, len(unsettled))
		for i, head := range unsettled {
			shas[i] = head.Sha
		}
		reads, err := client.CommitsCI(ctx, repo, shas)
		if err != nil {
			return pass, err
		}
		for _, head := range unsettled {
			ci := reads[head.Sha]
			// A head with no checks may be one GitHub has not started them for, so none settles it only after the grace from its own first read.
			if ci.Result == contract.PrCISuccess || ci.Result == contract.PrCIFailure || ci.Result == contract.PrCICancelled || ci.Result == contract.PrCINone && !now.Before(head.SeenAt.Add(ciGrace)) {
				pass.MainCIs = append(pass.MainCIs, store.MainCI{Repo: repo, Branch: probe.Branch, Head: head.Sha, Result: ci.Result, Runs: ci.Runs, RunsTruncated: ci.Truncated})
			}
		}
	}
	pass.Feeds = probe.Feeds
	return pass, nil
}

// report logs a repository's failure when it changes.
func (p *Poller) report(repo contract.RepoName, err error) {
	text := ""
	if err != nil && !errors.Is(err, ErrPaused) {
		text = err.Error()
	}
	if text != "" && text != p.failed[repo] {
		log.Printf("github %s: %v", repo, err)
	}
	p.failed[repo] = text
}

// Pass reads every due repository, then writes all they found at once, so one pass gives a session one wake.
func (p *Poller) Pass(ctx context.Context) error {
	pulls, err := p.Store.TrackedPRs(ctx)
	if err != nil {
		return err
	}
	issues, err := p.Store.TrackedIssues(ctx)
	if err != nil {
		return err
	}
	heads, err := p.Store.RepoHeads(ctx, p.Now())
	if err != nil {
		return err
	}
	feeds, err := p.Store.GitHubFeeds(ctx)
	if err != nil {
		return err
	}
	repos := map[contract.RepoName]bool{}
	for repo := range heads {
		repos[repo] = true
	}
	pullsIn := map[contract.RepoName][]store.TrackedPR{}
	for _, row := range pulls {
		repos[row.Repo] = true
		pullsIn[row.Repo] = append(pullsIn[row.Repo], row)
	}
	issuesIn := map[contract.RepoName][]store.TrackedIssue{}
	for _, row := range issues {
		repos[row.Repo] = true
		issuesIn[row.Repo] = append(issuesIn[row.Repo], row)
	}
	due := p.due(ctx, repos)
	if len(due) == 0 {
		return nil
	}
	if p.Accounts == nil {
		if err := p.GitHub.LoadViewer(ctx); err != nil {
			if errors.Is(err, ErrPaused) {
				return nil
			}
			return err
		}
	}
	var pass store.GitHubPass
	for _, repo := range due {
		found, err := p.repository(ctx, repo, pullsIn[repo], issuesIn[repo], heads, feeds[repo])
		p.report(repo, err)
		if err != nil {
			continue
		}
		pass.PRs = append(pass.PRs, found.PRs...)
		pass.Issues = append(pass.Issues, found.Issues...)
		pass.Heads = append(pass.Heads, found.Heads...)
		pass.MainCIs = append(pass.MainCIs, found.MainCIs...)
		pass.Feeds = append(pass.Feeds, found.Feeds...)
		pass.Items = append(pass.Items, found.Items...)
	}
	if len(pass.PRs)+len(pass.Issues)+len(pass.Heads)+len(pass.MainCIs)+len(pass.Feeds)+len(pass.Items) == 0 {
		return nil
	}
	return p.Store.ApplyPRPoll(ctx, pass, p.Now())
}
