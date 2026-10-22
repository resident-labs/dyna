package subagent

import "dyna-agent/internal/pr/review"

// reviewerHierarchy adapts the session store without moving lock ownership.
type reviewerHierarchy struct {
	store *Store
}

func (h reviewerHierarchy) ReadMeta(id string) (review.Session, error) {
	meta, err := h.store.ReadMeta(id)
	return reviewerSession(meta), err
}

func (h reviewerHierarchy) LiveSessions() ([]review.Session, error) {
	all, err := h.store.ListSnapshot(false)
	if err != nil {
		return nil, err
	}
	sessions := make([]review.Session, 0, len(all))
	for _, info := range all {
		sessions = append(sessions, reviewerSession(info.Meta))
	}
	return sessions, nil
}

func reviewerSession(meta Meta) review.Session {
	return review.Session{Role: meta.Role, Profile: meta.Profile, ParentSession: meta.ParentSession}
}
