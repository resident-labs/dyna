package prs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const feedRoomRecovery = "room_pr_recovery"

// roomPRs shares the repository feeds. Only the initial census lists every open PR;
// a gap lists just recently updated open PRs. Detail reads enrich, but never block, the feed snapshots.
func (p *Poller) roomPRs(ctx context.Context, client *Client, repo contract.RepoName, probe *Probe, recovery store.GitHubFeed, now time.Time, observed map[int]*Observed) ([]store.RoomPRPass, error, error) {
	watches, err := p.Store.RoomPRWatches(ctx, repo)
	if err != nil || len(watches) == 0 {
		return nil, nil, err
	}
	listed := maps.Clone(probe.Seen)
	census := slices.ContainsFunc(watches, func(w store.RoomPRWatch) bool { return w.CensusAt == 0 })
	var degraded error
	from := recovery.Cursor.At
	if !probe.GapFrom.IsZero() && (from.IsZero() || probe.GapFrom.Before(from)) {
		from = probe.GapFrom
	}
	censusOnly := map[int]bool{}
	if census || !from.IsZero() {
		next := fmt.Sprintf("/repos/%s/pulls?state=open&sort=updated&direction=desc&per_page=30", repo)
		for next != "" {
			header, data, err := client.get(ctx, next, "")
			var items []restItem
			if err == nil {
				err = json.Unmarshal(data, &items)
			}
			if err != nil {
				degraded = fmt.Errorf("open PR list: %w", err)
				break
			}
			next = nextPage(header.Get("Link"))
			for _, item := range items {
				old := from.IsZero() || item.UpdatedAt.Before(from)
				if !census && old {
					next = ""
					break
				}
				if listed[item.Number] == nil {
					f := item.feedItem()
					f.Kind = contract.GitHubPR
					listed[f.Number] = &f
					censusOnly[f.Number] = old
				}
			}
		}
	}
	censusComplete := census && degraded == nil
	// Retain the lower bound on an incomplete recovery, independently of live feed cursors.
	if degraded == nil {
		from = time.Time{}
	}
	if !from.Equal(recovery.Cursor.At) {
		probe.Feeds = append(probe.Feeds, store.GitHubFeed{Repo: repo, Feed: feedRoomRecovery, Cursor: store.FeedCursor{At: from}})
	}
	actions := map[int][]contract.GitHubItem{}
	for _, item := range probe.Items {
		actions[item.Number] = append(actions[item.Number], item)
	}
	numbers := map[int]bool{}
	for _, w := range watches {
		for n, row := range w.Rows {
			if (row.State == contract.PrOpen || reopens(actions[n])) && (listed[n] != nil || len(actions[n]) > 0 || needsRead(row, now)) {
				numbers[n] = true
			}
		}
		for n, f := range listed {
			if f.Kind == contract.GitHubPR && (w.CensusAt == 0 || !censusOnly[n]) {
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
	if observed == nil {
		observed = map[int]*Observed{}
	}
	// Keep successful chunks and let feed snapshots through if a detail query fails.
	slices.Sort(ns)
	for batch := range slices.Chunk(ns, 10) {
		extra, err := client.Read(ctx, repo, batch)
		if err != nil {
			degraded = errors.Join(degraded, fmt.Errorf("PR details: %w", err))
			break
		}
		maps.Copy(observed, extra)
	}
	var passes []store.RoomPRPass
	for _, w := range watches {
		initialized := w.CensusAt != 0
		pass := store.RoomPRPass{Room: w.Room, Repo: repo, CensusComplete: !initialized && censusComplete}
		for n := range numbers {
			row, exists := w.Rows[n]
			if !exists {
				f := listed[n]
				if f == nil || f.Kind != contract.GitHubPR || f.State != "open" || initialized && censusOnly[n] {
					continue
				}
				opened := slices.ContainsFunc(actions[n], func(item contract.GitHubItem) bool { return item.Action == contract.GitHubOpened })
				row = store.TrackedPR{
					PrSummary: contract.PrSummary{Repo: repo, Number: n, State: contract.PrOpen},
					Baseline:  !initialized && !opened,
				}
			}
			update, changed := ComparePR(row, actions[n], listed[n], observed[n], client.Viewer(), now)
			if observed[n] == nil && degraded != nil && update.PR.State == contract.PrOpen && !update.Meta.Recheck {
				update.Meta.Recheck = true
				changed = true
			}
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
	return passes, degraded, ctx.Err()
}
