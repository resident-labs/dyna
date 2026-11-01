package prs

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	firstWait   = time.Minute
	longestWait = 10 * time.Minute
	// lowQuotaWait is the shortest gap between polls of a repository with under 20% of the quota left.
	lowQuotaWait = 10 * time.Minute
)

// Poller reads each repository a room lists or a tracked open PR sits on, by the back-off of spec 7.7. The schedule lives
// in memory: an engine restart polls once and starts from a minute again.
type Poller struct {
	Store  *store.Store
	GitHub *Client
	Now    func() time.Time

	mu    sync.Mutex
	slots map[contract.RepoName]*slot
	// bases maps a repository to the bases of the PRs whose heads it holds, so a push to a fork finds its upstream PR.
	bases map[contract.RepoName][]contract.RepoName
}

// slot is one repository's schedule. A zero wait means it has not been polled.
type slot struct {
	wait time.Duration
	last time.Time
	next time.Time
	main string
}

// Run polls on each tick until ctx ends.
func (p *Poller) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := p.Pass(ctx); err != nil && ctx.Err() == nil {
				log.Printf("pr poll: %v", err)
			}
		}
	}
}

// Reset brings a repository back to the first wait, after a push or a new PR, and with it every repository a PR from it
// merges into. The next poll is at most a minute away, however often this runs, so pushes cannot outrun the budget.
func (p *Poller) Reset(repo contract.RepoName) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	for _, base := range append([]contract.RepoName{repo}, p.bases[repo]...) {
		item := p.slots[base]
		if item == nil || item.wait == 0 {
			continue
		}
		item.wait = firstWait
		if at := now.Add(firstWait); item.next.After(at) {
			item.next = at
		}
	}
}

// due refreshes the schedule from the tracked PRs and listed repositories, and returns the repositories to poll now.
func (p *Poller) due(rows []store.TrackedPR, heads map[contract.RepoName]string) []contract.RepoName {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slots == nil {
		p.slots = map[contract.RepoName]*slot{}
	}
	polled := map[contract.RepoName]bool{}
	for repo := range heads {
		polled[repo] = true
	}
	bases := map[contract.RepoName][]contract.RepoName{}
	for _, row := range rows {
		polled[row.Repo] = true
		if row.HeadRepo != "" && row.HeadRepo != row.Repo && !slices.Contains(bases[row.HeadRepo], row.Repo) {
			bases[row.HeadRepo] = append(bases[row.HeadRepo], row.Repo)
		}
	}
	p.bases = bases
	for repo := range polled {
		if p.slots[repo] == nil {
			p.slots[repo] = &slot{}
		}
	}
	for repo := range p.slots {
		if !polled[repo] {
			delete(p.slots, repo)
		}
	}
	now := p.Now()
	_, low := p.GitHub.Low()
	var due []contract.RepoName
	for repo, item := range p.slots {
		if now.Before(item.next) || low && item.wait != 0 && now.Before(item.last.Add(lowQuotaWait)) {
			continue
		}
		due = append(due, repo)
	}
	slices.Sort(due)
	return due
}

// settle moves a repository's schedule after a poll.
func (p *Poller) settle(repo contract.RepoName, moved bool, main string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item := p.slots[repo]
	if item.main != "" && main != "" && item.main != main {
		moved = true
	}
	if main != "" {
		item.main = main
	}
	switch {
	case moved || item.wait == 0:
		item.wait = firstWait
	default:
		item.wait = min(item.wait*2, longestWait)
	}
	item.last = p.Now()
	item.next = item.last.Add(item.wait)
}

// Pass polls every due repository, then writes all their changes at once, so one pass gives a session one wake.
func (p *Poller) Pass(ctx context.Context) error {
	rows, err := p.Store.TrackedPRs(ctx)
	if err != nil {
		return err
	}
	heads, err := p.Store.RepoHeads(ctx)
	if err != nil {
		return err
	}
	due := p.due(rows, heads)
	var updates []store.PRUpdate
	var moves []store.RepoHead
	for _, repo := range due {
		var numbers []int
		for _, row := range rows {
			if row.Repo == repo {
				numbers = append(numbers, row.Number)
			}
		}
		snapshot, err := p.GitHub.Poll(ctx, repo, numbers)
		if errors.Is(err, ErrPaused) {
			continue
		}
		if err != nil {
			log.Printf("pr poll %s: %v", repo, err)
			p.settle(repo, false, "")
			continue
		}
		moved := false
		for _, row := range rows {
			seen, ok := snapshot.PRs[row.Number]
			if row.Repo != repo || !ok {
				continue
			}
			update, changed := Compare(row, seen, snapshot.Viewer, p.Now())
			if changed {
				updates = append(updates, update)
				moved = true
			}
		}
		if stored, listed := heads[repo]; listed && snapshot.Main != "" && snapshot.Main != stored {
			moves = append(moves, store.RepoHead{Repo: repo, Head: snapshot.Main})
		}
		p.settle(repo, moved, snapshot.Main)
	}
	if len(updates) == 0 && len(moves) == 0 {
		return nil
	}
	return p.Store.ApplyPRPoll(ctx, updates, moves, p.Now())
}
