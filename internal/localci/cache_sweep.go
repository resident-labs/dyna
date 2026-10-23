package localci

// The CI machines keep running out of disk space because a run that crashed
// left its directory behind. Every run now removes its own directory, so this
// sweep only clears leftovers, once a day per machine, from an engine worker.

import (
	"context"
	"errors"
	"log"
	"time"
)

// cacheCleanEvery is how often one CI machine's own caches are cleaned.
const cacheCleanEvery = 24 * time.Hour

// cacheCleanRetry is how soon a failed cleanup is tried again.
const cacheCleanRetry = time.Hour

// CleanCachesTick cleans the caches of every enabled CI machine that has no
// cleanup in the last day, and returns when the next cleanup is due.
func CleanCachesTick(ctx context.Context, dataDir string) (time.Time, error) {
	store := Store{DataDir: dataDir}
	nodes, err := store.Nodes(ctx)
	if err != nil {
		return time.Time{}, err
	}
	runner := Runner{DataDir: dataDir}
	now := time.Now()
	var next time.Time
	soonest := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	var errs []error
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		last, err := store.LastCacheClean(ctx, node.Name)
		if err != nil {
			errs = append(errs, err)
			soonest(now.Add(cacheCleanRetry))
			continue
		}
		if due := last.Add(cacheCleanEvery); now.Before(due) {
			soonest(due)
			continue
		}
		cleaned, err := runner.CleanCaches(ctx, node, false)
		if err != nil {
			errs = append(errs, err)
			soonest(now.Add(cacheCleanRetry))
			continue
		}
		log.Printf("local CI %s: daily cache cleanup freed %d bytes from %d paths", node.Name, cleaned.FreedBytes, len(cleaned.Paths))
		if err := store.SaveCacheClean(ctx, node.Name, now); err != nil {
			errs = append(errs, err)
			soonest(now.Add(cacheCleanRetry))
			continue
		}
		soonest(now.Add(cacheCleanEvery))
	}
	return next, errors.Join(errs...)
}
