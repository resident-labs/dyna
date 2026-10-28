package roomsvc

import "dyna-agent/internal/listpage"

// Opened is everything a room view needs to draw its first screen, in one
// reply. A client that opens a room used to make five calls in a chain; over
// a tunnel each call costs a round trip.
type Opened struct {
	Feed    Feed     `json:"feed"`
	Members []Member `json:"members"`
	Choices []string `json:"choices"`
	Pins    []Post   `json:"pins"`
	Runs    RunList  `json:"runs"`
}

// Open reads a room once and returns its newest posts (tail, or the shared default
// when tail <= 0), members, composer choices, pins and runs. It shares one
// scan of the session store and one shared room-log index between them.
func (s *Service) Open(ref string, tail int, withClosed bool) (Opened, error) {
	tail = listpage.Limit(tail)
	store := s.rooms()
	rec, err := store.Resolve(ref)
	if err != nil {
		return Opened{}, err
	}
	all, rev := s.snapshot()
	book := s.avatarBook(rec.ID, all, rev)
	members := s.membersOf(rec, all, rev, true)
	var out Opened
	if out.Feed, err = s.roomFeed(rec, tail, "", book, members, s.rooms().ReadPage); err != nil {
		return Opened{}, err
	}
	out.Members = members
	if !withClosed {
		out.Members = s.membersOf(rec, all, rev, false)
	}
	// Choices complete open sessions only, whatever the member list shows.
	open := out.Members
	if withClosed {
		open = s.membersOf(rec, all, rev, false)
	}
	var names []string
	for _, m := range open[1:] {
		if m.Closed {
			continue
		}
		names = append(names, m.Name)
	}
	elsewhere, err := s.otherRoomChoices(rec, all)
	if err != nil {
		return Opened{}, err
	}
	out.Choices = append(Choices(names), elsewhere...)
	if out.Pins, err = s.pinsOf(rec, book); err != nil {
		return Opened{}, err
	}
	if out.Runs, err = s.Runs(rec.ID); err != nil {
		return Opened{}, err
	}
	return out, nil
}
