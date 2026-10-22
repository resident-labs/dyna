package cibudget

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"
)

// maxIsolated bounds the extra work one report can add to a CI job.
const maxIsolated = 50

// Isolate measures again each test that took longer than max, one at a time and
// alone, because a loaded runner slows fast tests. The shorter time replaces the
// reported one, so the budget judges the test and not the runner. A failed
// measurement keeps the reported time. When ctx ends, the remaining tests keep
// their reported times. Both times go to output. The report records which tests
// a run alone measured, so a tracker can tell a slow test from a slow host.
func (r *Report) Isolate(ctx context.Context, max time.Duration, alone func(context.Context, Test) (time.Duration, error), output io.Writer) {
	// The report now records which times a run alone confirmed. A test over
	// max that no isolated run confirmed keeps its full-run time, which a busy
	// runner inflates, so a reader must be able to tell the two apart.
	r.IsolationRan = true
	for i := range r.Tests {
		// Every test in this report was measured with isolated re-runs, so an
		// unconfirmed time over the limit is the load, not the test.
		r.Tests[i].IsolationRan = true
	}
	var slow []int
	for i, test := range r.Tests {
		if test.Duration > max {
			slow = append(slow, i)
		}
	}
	sort.SliceStable(slow, func(a, b int) bool { return r.Tests[slow[a]].Duration > r.Tests[slow[b]].Duration })
	if len(slow) > maxIsolated {
		fmt.Fprintf(output, "Isolated runs limited to the %d slowest of %d tests over %s\n", maxIsolated, len(slow), max)
		slow = slow[:maxIsolated]
	}
	for _, i := range slow {
		if ctx.Err() != nil {
			fmt.Fprintf(output, "Isolated runs stopped: %v\n", ctx.Err())
			return
		}
		test := r.Tests[i]
		duration, err := alone(ctx, test)
		if err != nil {
			fmt.Fprintf(output, "ISOLATED %s: %s in the full run; isolated run failed: %v\n", test.ID(), test.Duration, err)
			continue
		}
		fmt.Fprintf(output, "ISOLATED %s: %s in the full run, %s alone\n", test.ID(), test.Duration, duration)
		// A successful run alone measured this test, so its time is confirmed
		// even when the loaded run happened to be quicker; the shorter time is
		// the one that counts either way.
		r.Tests[i].Isolated = true
		if duration < test.Duration {
			r.Tests[i].Duration = duration
		}
	}
}
