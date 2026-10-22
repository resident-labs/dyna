package view

import (
	"maps"
	"reflect"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

// sameProjectionInputs ignores only poll accounting that neither published
// rows nor worker inputs use. It never changes the current or retained state.
// Other fields remain conservative invalidations, including quota pauses.
func sameProjectionInputs(a, b watch.State) bool {
	if len(a.Records) != len(b.Records) || !reflect.DeepEqual(projectionMetadata(a), projectionMetadata(b)) {
		return false
	}
	for key, left := range a.Records {
		right, found := b.Records[key]
		if !found {
			return false
		}
		if left == right {
			continue
		}
		if left == nil || right == nil || !reflect.DeepEqual(projectionRecord(*left), projectionRecord(*right)) {
			return false
		}
	}
	return true
}

func projectionRecord(record watch.Record) watch.Record {
	// Local rows still read external CI results. Keep their existing poll
	// invalidation until the canonical stored producer replaces that read.
	if record.Kind == "local" {
		return record
	}
	record.Checked, record.ChecksAt, record.HeadVerifiedAt = time.Time{}, time.Time{}, time.Time{}
	record.Polling = state.Polling{}
	record.RateLimit = projectionQuota(record.RateLimit)
	return record
}

func projectionMetadata(st watch.State) watch.State {
	st.Records, st.Responses = nil, nil
	st.RateLimit = projectionQuota(st.RateLimit)
	st.RateLimits = maps.Clone(st.RateLimits)
	for key, rate := range st.RateLimits {
		st.RateLimits[key] = projectionQuota(rate)
	}
	st.RepositoryFacts = maps.Clone(st.RepositoryFacts)
	for key, facts := range st.RepositoryFacts {
		facts.CheckedAt = time.Time{}
		st.RepositoryFacts[key] = facts
	}
	return st
}

func projectionQuota(rate state.RateLimit) state.RateLimit {
	rate.Remaining = nil
	rate.Reset, rate.ObservedAt = time.Time{}, time.Time{}
	return rate
}
