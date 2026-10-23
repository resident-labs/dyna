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
// saved path disappears before finishUpdate retires it.
func (s *Service) savePollResponses(key string, gh GitHub) error {
	if gh.requests == nil {
		return nil
	}
	unlock := gh.requests.lockRate()
	used := maps.Clone(gh.requests.used)
	rates := cloneRates(gh.requests.rates)
	unlock()
	if len(used) == 0 {
		return nil
	}
	return s.Store.updateRecordOrAll(key, func(current *State) bool {
		return ratesNeedAllRecords(current, rates, s.now())
	}, func(current *State) error {
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
		applyRateLimits(current, rates, s.now())
		return nil
	})
}
