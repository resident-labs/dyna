package view

import "dyna-agent/internal/pr/state"

// IssueFixes maps a room issue number to the open PR that closes it on merge.
// A closed or merged PR no longer fixes an issue, so only open changes count.
func (s *Service) IssueFixes(roomID string) (map[int]int, error) {
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, r := range st.Records {
		if r.RoomID != roomID || r.State != "open" || r.Number == 0 {
			continue
		}
		for _, fix := range r.Fixes {
			if fix.Kind != state.IssueKindDyna {
				continue
			}
			if _, ok := out[fix.Number]; !ok {
				out[fix.Number] = r.Number
			}
		}
	}
	return out, nil
}
