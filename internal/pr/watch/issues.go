package watch

import "dyna-agent/internal/pr/state"

func (s *Store) SaveIssueWork(work state.IssueWork) error {
	return s.update(func(st *State) error {
		if st.IssueWork == nil {
			st.IssueWork = map[string]state.IssueWork{}
		}
		st.IssueWork[state.IssueWorkKey(work.RoomID, work.Repo, work.Number)] = work
		return nil
	})
}
