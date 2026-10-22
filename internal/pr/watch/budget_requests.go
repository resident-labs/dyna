package watch

import (
	"bytes"
	"reflect"
	"time"

	"dyna-agent/internal/pr/state"
)

func sameResponse(a, b cachedResponse) bool {
	return a.ETag == b.ETag && a.Next == b.Next && bytes.Equal(a.Body, b.Body)
}

func budgetResponsesChanged(entries, used map[string]cachedResponse) bool {
	for path, response := range used {
		if previous, ok := entries[path]; !ok || !sameResponse(previous, response) {
			return true
		}
	}
	return false
}

// budgetRatesChanged keeps account observations independent of any PR's identity.
func budgetRatesChanged(st State, rates map[string]RateLimit, now time.Time) bool {
	for key, rate := range rates {
		previous := st.RateLimits[key]
		if rate.ObservedAt.Before(previous.ObservedAt) || previous.Reset.After(rate.Reset) {
			continue
		}
		if previous.Reset.Equal(rate.Reset) && previous.Remaining != nil && rate.Remaining != nil && *previous.Remaining < *rate.Remaining {
			rate.Remaining = previous.Remaining
		}
		if !reflect.DeepEqual(previous, rate) {
			return true
		}
	}
	return !st.LegacyPause.IsZero() && !now.Before(st.LegacyPause)
}

func (s *Service) saveBudgetRequests(result pollResult, r Record, policy ChangePolicy, observed state.Snapshot) error {
	requests := result.gh.requests
	if requests == nil {
		return nil
	}
	unlock := requests.lockRate()
	rates := cloneRates(requests.rates)
	unlock()
	metadata := observed.Metadata()
	live, ok := observed.Record(result.key)
	responsesChanged := ok && r.Key == result.key && budgetRecordMatches(metadata, &live, r, policy) &&
		budgetResponsesChanged(observed.Responses(result.key), requests.used)
	if !responsesChanged && !budgetRatesChanged(metadata, rates, s.now()) {
		return nil
	}
	return s.Store.updateRecordOrAll(result.key, func(current *State) bool {
		return ratesNeedAllRecords(current, rates, s.now())
	}, func(current *State) error {
		live := current.Records[result.key]
		responsesChanged := r.Key == result.key && budgetRecordMatches(*current, live, r, policy) &&
			budgetResponsesChanged(current.Responses[result.key], requests.used)
		ratesChanged := budgetRatesChanged(*current, rates, s.now())
		if !responsesChanged && !ratesChanged {
			return errNoStoreChange
		}
		if ratesChanged {
			applyRateLimits(current, rates, s.now())
		}
		if !responsesChanged {
			return nil
		}
		if current.Responses == nil {
			current.Responses = map[string]map[string]cachedResponse{}
		}
		entries := current.Responses[result.key]
		if entries == nil {
			entries = map[string]cachedResponse{}
		}
		for path, response := range requests.used {
			entries[path] = response
		}
		current.Responses[result.key] = entries
		return nil
	})
}
