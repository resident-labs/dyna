package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// roomService records the room each method received.
type roomService struct {
	fakeService
	rooms map[contract.Method]contract.RoomID
}

func (f *roomService) EventsList(_ context.Context, _ contract.Principal, r contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
	f.rooms[contract.MethodEventsList] = r.Room
	return contract.EventsListResponse{}, nil
}
func (f *roomService) SessionList(_ context.Context, _ contract.Principal, r contract.SessionListRequest) (contract.SessionListResponse, *contract.Error) {
	f.rooms[contract.MethodSessionList] = r.Room
	return contract.SessionListResponse{}, nil
}
func (f *roomService) SessionStart(_ context.Context, _ contract.Principal, r contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
	f.rooms[contract.MethodSessionStart] = r.Room
	return contract.SessionStartResponse{}, nil
}
func (f *roomService) SessionRefresh(_ context.Context, _ contract.Principal, r contract.SessionRefreshRequest) (contract.SessionRefreshResponse, *contract.Error) {
	f.rooms[contract.MethodSessionRefresh] = r.Room
	return contract.SessionRefreshResponse{}, nil
}
func (f *roomService) SubscriptionAdd(_ context.Context, _ contract.Principal, r contract.SubscriptionAddRequest) (contract.SubscriptionAddResponse, *contract.Error) {
	f.rooms[contract.MethodSubscriptionAdd] = r.Room
	return contract.SubscriptionAddResponse{}, nil
}
func (f *roomService) PrTrack(_ context.Context, _ contract.Principal, r contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error) {
	f.rooms[contract.MethodPrTrack] = r.Room
	return contract.PrTrackResponse{}, nil
}
func (f *roomService) IssueTrack(_ context.Context, _ contract.Principal, r contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error) {
	f.rooms[contract.MethodIssueTrack] = r.Room
	return contract.IssueTrackResponse{}, nil
}

func TestASessionsOmittedRoom(t *testing.T) {
	for _, tc := range []struct {
		method contract.Method
		body   string
		want   contract.RoomID
	}{
		{contract.MethodEventsList, `{}`, "room-a"},
		{contract.MethodSessionList, `{}`, "room-a"},
		{contract.MethodSessionStart, `{"name":"child","profile":"test"}`, "room-a"},
		{contract.MethodSubscriptionAdd, `{"scope":"room"}`, "room-a"},
		// These name their target another way, and a filled room would change what they do.
		{contract.MethodSessionRefresh, `{"session":"session-b"}`, ""},
		{contract.MethodSubscriptionAdd, `{"scope":"session","session":"session-a"}`, ""},
		{contract.MethodPrTrack, `{"number":1}`, ""},
		{contract.MethodIssueTrack, `{"number":1}`, ""},
	} {
		f := &roomService{rooms: map[contract.Method]contract.RoomID{}}
		w := request(New(f, nil).handler(boxCaller()), "/v1/"+string(tc.method), tc.body)
		if got, called := f.rooms[tc.method]; w.Code != http.StatusOK || !called || got != tc.want {
			t.Errorf("%s %s: status %d, room %q, want %q", tc.method, tc.body, w.Code, got, tc.want)
		}
	}
}

// An owner's omitted room means every room or none, so it stays omitted.
func TestAnOwnersOmittedRoomStaysOmitted(t *testing.T) {
	f := &roomService{rooms: map[contract.Method]contract.RoomID{}}
	owner := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}
	if w := request(New(f, nil).handler(owner), "/v1/events.list", `{}`); w.Code != http.StatusOK || f.rooms[contract.MethodEventsList] != "" {
		t.Fatalf("status %d, room %q", w.Code, f.rooms[contract.MethodEventsList])
	}
}

func TestAnotherRoomNamesTheCallersOwn(t *testing.T) {
	for _, tc := range []struct {
		method contract.Method
		body   string
	}{
		{contract.MethodEventsList, `{"room":"horizon"}`},
		{contract.MethodSessionList, `{"room":"horizon"}`},
		{contract.MethodOpSubmit, `{"operation":{"kind":"pr.open","room":"horizon","args":{"title":"t","body":"b","base":"main","draft":false}}}`},
	} {
		f := &roomService{rooms: map[contract.Method]contract.RoomID{}}
		w := request(New(f, nil).handler(boxCaller()), "/v1/"+string(tc.method), tc.body)
		var got contract.Error
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := `room "horizon" is not your room; yours is room-a`; w.Code != http.StatusForbidden || got.Message != want {
			t.Errorf("%s: status %d, %q", tc.method, w.Code, got.Message)
		}
	}
}
