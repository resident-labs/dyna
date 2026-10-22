package runtime

import (
	"context"
	"time"

	"dyna-agent/internal/profile"
)

// doctorBudget bounds one status pass, so a slow or blocked check cannot hold
// the Harnesses page or `dyna harness doctor`.
const doctorBudget = 8 * time.Second

// Doctors checks every profile in parallel. Profiles with the same harness and
// version share one check. A check that misses the budget reports
// "unavailable" and keeps running in the background. It still reports the
// harness's switch state, which a cheap lock check reads.
func (r *Runtime) Doctors(ctx context.Context, profiles []profile.Profile) []Doctor {
	switchState := func(p profile.Profile) Doctor {
		var d Doctor
		if p.DynaManaged() {
			d.SwitchPending, d.RunningTurns = r.Accounts.SwitchStatus(p.Harness)
		}
		return d
	}
	return doctors(ctx, profiles, doctorBudget, r.Doctor, switchState)
}

func doctors(ctx context.Context, profiles []profile.Profile, budget time.Duration, check func(context.Context, profile.Profile) Doctor, timedOut func(profile.Profile) Doctor) []Doctor {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type key struct{ harness, version string }
	keyFor := func(p profile.Profile) key {
		if !p.DynaManaged() {
			return key{harness: "\x00" + p.Name}
		}
		h, err := CanonicalHarness(p.Harness)
		if err != nil {
			h = p.Harness
		}
		return key{h, p.RuntimeVersion}
	}
	results := map[key]chan Doctor{}
	first := map[key]profile.Profile{}
	for _, p := range profiles {
		k := keyFor(p)
		if results[k] != nil {
			continue
		}
		first[k] = p
		done := make(chan Doctor, 1)
		results[k] = done
		go func() { done <- check(ctx, p) }()
	}
	shared := map[key]Doctor{}
	for k, done := range results {
		// A finished result wins over the expired budget.
		select {
		case shared[k] = <-done:
			continue
		default:
		}
		select {
		case shared[k] = <-done:
		case <-ctx.Done():
			d := timedOut(first[k])
			d.Status, d.Reason = "unavailable", "The status check did not finish in time. Refresh to try again."
			shared[k] = d
		}
	}
	out := make([]Doctor, 0, len(profiles))
	for _, p := range profiles {
		d := shared[keyFor(p)]
		d.Profile, d.Harness = p.Name, p.Harness
		out = append(out, d)
	}
	return out
}
