package localci

// The CI machines keep running out of disk space because a run that crashed
// left its directory behind. Every run now removes its own directory and holds
// a lock while it runs, so this sweep only clears leftovers, once an hour per
// machine, from an engine worker.

import (
	"context"
	"errors"
	"log"
	"time"
)

// cacheCleanEvery is how often one CI machine's own caches are cleaned.
const cacheCleanEvery = time.Hour

// cacheCleanRetry is how soon a failed cleanup is tried again.
const cacheCleanRetry = time.Hour

// CacheCleanReporter posts one bounded failure for a machine's cleanup pass.
type CacheCleanReporter func(context.Context, Node, CacheCleanError) error

// CleanCachesTick cleans the caches of every enabled CI machine that has no
// cleanup in the last hour, and returns when the next cleanup is due. A machine
// change wakes the worker, so a machine enabled later gets its own deadline.
// The same pass sweeps the machine's scratch folders and reports what it freed.
func CleanCachesTick(ctx context.Context, dataDir string, report ScratchReporter, reportFailure CacheCleanReporter) (time.Time, error) {
	return cleanCachesTick(ctx, dataDir, time.Now, func(ctx context.Context, node Node) (CacheCleanResult, error) {
		runner := Runner{DataDir: dataDir}
		cleaned, err := runner.CleanCaches(ctx, node, false)
		if err != nil && reportFailure != nil {
			reportErr := reportFailure(ctx, node, cacheCleanFailure(node, err))
			err = errors.Join(err, reportErr)
		}
		swept, scratchErr := sweepNodeScratch(ctx, runner, node, time.Now())
		if report != nil && swept.FreedBytes > 0 {
			scratchErr = errors.Join(scratchErr, report(ctx, node, swept))
		}
		// A scratch failure must not discard the cache result or its hourly
		// stamp, so it is logged and the next hour sweeps again.
		if scratchErr != nil {
			log.Printf("local CI %s: scratch sweep: %v", node.Name, scratchErr)
		}
		return cleaned, err
	})
}

func cleanCachesTick(ctx context.Context, dataDir string, now func() time.Time, clean func(context.Context, Node) (CacheCleanResult, error)) (time.Time, error) {
	store := Store{DataDir: dataDir}
	nodes, err := store.Nodes(ctx)
	if err != nil {
		return time.Time{}, err
	}
	at := now()
	var next time.Time
	soonest := func(when time.Time) {
		if next.IsZero() || when.Before(next) {
			next = when
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
			soonest(at.Add(cacheCleanRetry))
			continue
		}
		if due := last.Add(cacheCleanEvery); at.Before(due) {
			soonest(due)
			continue
		}
		cleaned, err := clean(ctx, node)
		if err != nil {
			errs = append(errs, err)
			soonest(at.Add(cacheCleanRetry))
			continue
		}
		log.Printf("local CI %s: cache cleanup freed %d bytes from %d paths", node.Name, cleaned.FreedBytes, len(cleaned.Paths))
		if err := store.SaveCacheClean(ctx, node.Name, at); err != nil {
			errs = append(errs, err)
			soonest(at.Add(cacheCleanRetry))
			continue
		}
		soonest(at.Add(cacheCleanEvery))
	}
	return next, errors.Join(errs...)
}
