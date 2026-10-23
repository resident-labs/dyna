package watch

import (
	"maps"
	"strings"
)

// mergePollClient returns a client for the merge queue's repeated reads. It
// revalidates the record's saved CI and comment responses, so an unchanged PR
// answers by 304 and costs no quota. The pull body itself is read fresh:
// GitHub can recompute mergeability without changing that body's validator,
// and a merge gate must never act on a stale verdict.
func (s *Service) mergePollClient(st State, r Record) (GitHub, error) {
	entries, err := s.Store.readRecordResponses(r.Key)
	if err != nil {
		return GitHub{}, err
	}
	entries = maps.Clone(entries)
	delete(entries, strings.ToLower(r.Host)+"/"+refRecord(r).path(""))
	gh := s.github(r.RoomID)
	gh.requests = &requestCache{
		entries:     entries,
		used:        map[string]cachedResponse{},
		rates:       st.RateLimits,
		legacyPause: st.LegacyPause,
		now:         s.now,
	}
	return gh, nil
}

// savePollResponses keeps the validated copies and the quota those replies
// prove. Entries merge, so a poll worker's newer copy is never dropped and no
// saved path disappears before finishUpdate retires it. Rate evidence is saved
// on its own: a reply that failed the request, such as a 403 rate limit on the
// first read, saves no copy but still proves the pause.
func (s *Service) savePollResponses(key string, gh GitHub) error {
	if gh.requests == nil {
		return nil
	}
	unlock := gh.requests.lockRate()
	used := maps.Clone(gh.requests.used)
	rates := cloneRates(gh.requests.rates)
	unlock()
	now := s.now()
	return s.Store.updateRecordOrAll(key, func(current *State) bool {
		return ratesNeedAllRecords(current, rates, now)
	}, func(current *State) error {
		ratesChanged := budgetRatesChanged(*current, rates, now)
		if len(used) == 0 && !ratesChanged {
			return errNoStoreChange
		}
		if ratesChanged {
			applyRateLimits(current, rates, now)
		}
		if len(used) == 0 {
			return nil
		}
		if current.Responses == nil {
			current.Responses = map[string]map[string]cachedResponse{}
		}
		entries := current.Responses[key]
		if entries == nil {
			entries = map[string]cachedResponse{}
		}
		for path, response := range used {
			entries[path] = response
		}
		current.Responses[key] = entries
		return nil
	})
}
