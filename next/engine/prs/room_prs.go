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

const (
	feedRoomRecovery = "room_pr_recovery"
	roomPRReadBudget = 20 * time.Second // census, recovery and room-only detail reads share this budget
)

// roomPRs shares the repository feeds. Only the initial census lists every open PR;
// a gap lists just recently updated PRs. Detail reads enrich, but never block, the feed snapshots.
func (p *Poller) roomPRs(ctx context.Context, client *Client, repo contract.RepoName, probe *Probe, feeds map[string]store.GitHubFeed, now time.Time, observed map[int]*Observed) ([]store.RoomPRPass, error, error) {
	watches, err := p.Store.RoomPRWatches(ctx, repo)
	if err != nil || len(watches) == 0 {
		return nil, nil, err
	}
	readCtx, cancel := context.WithTimeout(ctx, roomPRReadBudget)
	defer cancel()
	listed := maps.Clone(probe.Seen)
	var rooms []contract.RoomID
	for _, w := range watches {
		if w.CensusAt == 0 {
			rooms = append(rooms, w.Room)
		}
	}
	slices.Sort(rooms)
	var degraded error
	progress := feeds[feedRoomRecovery].Cursor
	from := progress.At
	if !probe.GapFrom.IsZero() && (from.IsZero() || probe.GapFrom.Before(from)) {
		from = probe.GapFrom
		progress.NextPage = ""
	}
	// A catch-up also lists recently closed PRs to reconcile known snapshots. If a
	// room needs its first census, list all open PRs on the next pass instead.
	census := len(rooms) > 0 && from.IsZero()
	next := ""
	if census || !from.IsZero() {
		state := "all"
		if census {
			state = "open"
		}
		next = progress.NextPage
		if census {
			saved := feeds[store.RoomPRCensusFeed].Cursor
			// A newly enabled room did not receive the preceding pages.
			if slices.Equal(rooms, saved.CensusRooms) {
				next = saved.NextPage
			}
		}
		if next == "" {
			next = fmt.Sprintf("/repos/%s/pulls?state=%s&sort=updated&direction=desc&per_page=30", repo, state)
		}
		for page := 0; next != ""; page++ {
			if page == maxFeedPages {
				degraded = fmt.Errorf("PR list: continuing next pass after %d pages", maxFeedPages)
				break
			}
			header, data, err := client.get(readCtx, next, "")
			var items []restItem
			if err == nil {
				err = json.Unmarshal(data, &items)
			}
			if err != nil {
				degraded = fmt.Errorf("PR list: %w", err)
				break
			}
			next = nextPage(header.Get("Link"))
			for _, item := range items {
				if !census && item.UpdatedAt.Before(from) {
					next = ""
					break
				}
				if listed[item.Number] == nil {
					f := item.feedItem()
					f.Kind = contract.GitHubPR
					listed[f.Number] = &f
				}
			}
		}
	}
	censusComplete := census && next == ""
	// Commit the continuation with the snapshots, independently of the live feed cursors.
	if census {
		saved := feeds[store.RoomPRCensusFeed].Cursor
		if next == "" {
			rooms = nil
		}
		if next != saved.NextPage || !slices.Equal(rooms, saved.CensusRooms) {
			probe.Feeds = append(probe.Feeds, store.GitHubFeed{Repo: repo, Feed: store.RoomPRCensusFeed, Cursor: store.FeedCursor{NextPage: next, CensusRooms: rooms}})
		}
	} else {
		if next == "" {
			from = time.Time{}
		}
		saved := feeds[feedRoomRecovery].Cursor
		if next != saved.NextPage || !from.Equal(saved.At) {
			probe.Feeds = append(probe.Feeds, store.GitHubFeed{Repo: repo, Feed: feedRoomRecovery, Cursor: store.FeedCursor{At: from, NextPage: next}})
		}
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
			if f.Kind == contract.GitHubPR && (w.CensusAt == 0 || !census || probe.Seen[n] != nil) {
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
		extra, err := client.Read(readCtx, repo, batch)
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
				if f == nil || f.Kind != contract.GitHubPR || f.State != "open" || initialized && census && probe.Seen[n] == nil {
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
