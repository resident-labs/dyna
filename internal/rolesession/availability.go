package rolesession

import (
	"errors"
	"slices"
	"time"

	runtime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// Availability is a profile's current evidence, not a guarantee of a future turn.
type Availability struct {
	Profile    string
	State      runtime.QuotaAvailability
	EvidenceAt time.Time
	Error      error
}

// SessionAvailability reads uncached account and turn evidence for agent sessions.
// Unknown means that the saved evidence cannot establish readiness.
func SessionAvailability(profiles []profile.Profile, repo string, sessions *subagent.Store, now time.Time) []Availability {
	var readErr error
	failures, scanErr := (&FailureCache{}).Recent(now, func() ([]subagent.Info, error) {
		infos, err := sessions.ListSnapshot(true)
		if err != nil {
			return nil, err
		}
		return slices.DeleteFunc(infos, func(i subagent.Info) bool { return i.Turns == 0 }), nil
	}, func(id string, n int) (subagent.Turn, error) {
		t, err := sessions.ReadTurn(id, n)
		readErr = errors.Join(readErr, err)
		return t, err
	})
	scanErr = errors.Join(scanErr, readErr)
	accounts := runtime.NewAccounts()
	bundles := runtime.NewBundleManager()
	out := make([]Availability, 0, len(profiles))
	for _, p := range profiles {
		a := Availability{Profile: p.Name, State: runtime.QuotaUnknown}
		switch {
		case p.Disabled || !p.ForSubagents():
			a.State = runtime.QuotaUnavailable
		case scanErr != nil:
			a.Error = scanErr
		default:
			if err := p.Validate(); err != nil {
				a.State = runtime.QuotaUnavailable
				a.Error = err
				break
			}
			adapter, err := runtime.Adapter(p.Harness)
			if err != nil {
				a.Error = err
				break
			}
			if cause, _ := blockReason(p, failures, now); cause != "" {
				a.State = runtime.QuotaUnavailable
				break
			}
			if adapter.Exclusive {
				pending, running := accounts.SwitchStatus(adapter.Harness)
				if pending || running > 0 {
					a.State = runtime.QuotaUnavailable
					break
				}
			}
			if _, err := bundles.Resolve(p.Harness, p.RuntimeVersion); err != nil {
				a.Error = err
				break
			}
			// A missing repository identity cannot establish which accounts may run.
			if repo == "" {
				break
			}
			evidence, err := accounts.QuotaAvailability(p.Harness, runtime.ProviderForModel(p.Harness, p.Model), repo, now)
			a.State, a.EvidenceAt, a.Error = evidence.State, evidence.ObservedAt, errors.Join(a.Error, err)
		}
		out = append(out, a)
	}
	return out
}
