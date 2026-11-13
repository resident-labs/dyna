package prs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"time"
)

// roomPRs shares the repository feeds. Only the initial census lists every open PR;
// subsequent passes detail-read changed PRs and unsettled checks, as session tracking does.
func (p *Poller) roomPRs(ctx context.Context, client *Client, repo contract.RepoName, probe Probe, now time.Time, observed map[int]*Observed) ([]store.RoomPRPass, error) {
	watches, err := p.Store.RoomPRWatches(ctx, repo)
	if err != nil || len(watches) == 0 {
		return nil, err
	}
	listed := map[int]*FeedItem{}
	for n, item := range probe.Seen {
		listed[n] = item
	}
	census := false
	for _, w := range watches {
		if w.CensusAt == 0 {
			census = true
		}
	}
	if census {
		for page := 1; ; page++ {
			_, data, err := client.get(ctx, fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100&page=%d", repo, page), "")
			if err != nil {
				return nil, err
			}
			var items []restItem
			if err := json.Unmarshal(data, &items); err != nil {
				return nil, err
			}
			for _, item := range items {
				f := item.feedItem()
				f.Kind = contract.GitHubPR
				listed[f.Number] = &f
			}
			if len(items) < 100 {
				break
			}
		}
	}
	actions := map[int][]contract.GitHubItem{}
	for _, item := range probe.Items {
		actions[item.Number] = append(actions[item.Number], item)
	}
	numbers := map[int]bool{}
	for _, w := range watches {
		for n, row := range w.Rows {
			if (row.State == contract.PrOpen || reopens(actions[n])) && (listed[n] != nil || len(actions[n]) > 0 || probe.Resync || needsRead(row, now)) {
				numbers[n] = true
			}
		}
		for n, f := range listed {
			if f.Kind == contract.GitHubPR {
				if _, ok := w.Rows[n]; !ok && f.State == "open" {
					numbers[n] = true
				}
			}
		}
	}
	ns := []int{}
	for n := range numbers {
		if observed[n] == nil {
			ns = append(ns, n)
		}
	}
	extra, err := client.Read(ctx, repo, ns)
	if err != nil {
		return nil, err
	}
	if observed == nil {
		observed = map[int]*Observed{}
	}
	for n, item := range extra {
		observed[n] = item
	}
	var passes []store.RoomPRPass
	for _, w := range watches {
		initialized := w.CensusAt != 0
		pass := store.RoomPRPass{Room: w.Room, Repo: repo, CensusComplete: !initialized}
		for n := range numbers {
			row, exists := w.Rows[n]
			if !exists {
				f := listed[n]
				if f == nil || f.Kind != contract.GitHubPR || f.State != "open" {
					continue
				}
				row = store.TrackedPR{PrSummary: contract.PrSummary{Repo: repo, Number: n, State: contract.PrOpen}, Baseline: !initialized}
			}
			update, changed := ComparePR(row, actions[n], listed[n], observed[n], client.Viewer(), now)
			if !changed {
				continue
			}
			next := store.TrackedPR{PrSummary: update.PR, HeadRepo: update.HeadRepo, CITold: update.CITold, SeenReviews: update.SeenReviews, OwnWakeAt: update.OwnWakeAt, Meta: update.Meta, MetaWakeAt: update.MetaWakeAt}
			pass.Rows = append(pass.Rows, next)
			if update.Change != nil {
				if !exists {
					update.Change.Kinds = append(update.Change.Kinds, contract.PrChangeOpened)
				}
				pass.Changes = append(pass.Changes, *update.Change)
			}
		}
		passes = append(passes, pass)
	}
	return passes, nil
}
