package state

import (
	"sort"
	"time"
)

// AuthoredBy returns the room's PRs that session authors: the open ones, and
// the ones merged or closed after since, by number.
func (s State) AuthoredBy(roomID, session string, since time.Time) []*Record {
	var out []*Record
	for _, r := range s.Records {
		if r != nil && r.RoomID == roomID && r.Author == session && (r.State == PROpen || !r.ClosedAt.Before(since)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
