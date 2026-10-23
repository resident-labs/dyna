package librarianstate

import "dyna-agent/internal/authority"

// CheckEdit applies the mutation rule using the repository's saved librarian.
// Actor comes from the caller, even when an owner process handles the request.
func (s Store) CheckEdit(action, repo string, actor authority.Actor) error {
	return authority.Check(action, actor, func(relation authority.Relation) bool {
		if relation != authority.Librarian || actor.Session == "" {
			return false
		}
		state, err := s.Get(repo)
		return err == nil && state.Session == actor.Session
	})
}

// CheckSmell permits only the repository's saved code-reading smell session.
// Map mutations continue to use CheckEdit and its separate saved session.
func (s Store) CheckSmell(action, repo string, actor authority.Actor) error {
	return authority.Check(action, actor, func(relation authority.Relation) bool {
		if relation != authority.Librarian || actor.Session == "" {
			return false
		}
		state, err := s.Get(repo)
		return err == nil && state.SmellSession == actor.Session
	})
}
