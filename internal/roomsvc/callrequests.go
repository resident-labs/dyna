package roomsvc

import (
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/listchange"
)

// CallRequests returns active rooms, or the complete history of one room.
func (s *Service) CallRequests(ref string) (map[string][]callrequests.Request, error) {
	out := map[string][]callrequests.Request{}
	if ref != "" {
		rec, err := s.rooms().Resolve(ref)
		if err != nil {
			return nil, err
		}
		rows, err := s.Notify.CallRequestStore().List(rec.ID)
		if rows == nil {
			rows = []callrequests.Request{}
		}
		out[rec.ID] = rows
		return out, err
	}
	records, err := s.rooms().List(false)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		rows, err := s.Notify.CallRequestStore().List(rec.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.State == "ringing" {
				out[rec.ID] = append(out[rec.ID], row)
			}
		}
	}
	return out, nil
}

func (s *Service) CallRequestShown(ref, id string) (callrequests.Request, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return callrequests.Request{}, err
	}
	return s.Notify.CallRequestStore().Shown(rec.ID, id)
}

func (s *Service) DeclineCallRequest(ref, id string) (callrequests.Request, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return callrequests.Request{}, err
	}
	row, _, err := s.Notify.CallRequestStore().Close(rec.ID, id, "declined", "owner")
	return row, err
}

func (s *Service) scanCallRequests(signatures map[string]string, first bool, emit func(Event)) {
	rows, err := s.Notify.CallRequestStore().List("")
	if err != nil {
		return
	}
	items := make([]listchange.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, listchange.Item{Kind: "call-requests", RoomID: row.Room, ID: row.ID, Value: row})
	}
	changes, err := listchange.Sync(signatures, items, first)
	if err != nil {
		return
	}
	for _, change := range changes {
		emit(Event{Kind: change.Kind, RoomID: change.RoomID, Change: &change.Change})
	}
}
