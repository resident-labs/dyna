package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// An hourly wake as a live root asked for one: a duration string, and sessions by name or by ID.
func TestSubscriptionTakesSessionNamesAndDurationStrings(t *testing.T) {
	s, room, worker, ctx := setup(t)
	other, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	self := sessionCaller(worker)
	listed := func(id contract.SubscriptionID) contract.Subscription {
		t.Helper()
		list, problem := s.SubscriptionList(ctx, ownerCaller, contract.SubscriptionListRequest{Room: room})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, info := range list.Subscriptions {
			if info.Subscription.ID == id {
				return info.Subscription
			}
		}
		t.Fatalf("%s is not listed", id)
		return contract.Subscription{}
	}
	for _, tc := range []struct {
		name   string
		caller contract.Principal
		req    contract.SubscriptionAddRequest
		every  contract.Duration
	}{
		{"names, from the session", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "worker", Every: "1h", Wake: "worker"}, "1h"},
		{"IDs, from the session", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: string(worker), Every: "60m", Wake: string(worker)}, "1h"},
		{"a mention's @, from the session", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "@worker", Every: "90s", Wake: "@worker"}, "1m30s"},
		{"names in a room, from the owner", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Room: room, Session: "worker", Every: "15m", Wake: "worker"}, "15m"},
		{"a room wake by name, from the owner", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: "2h", Wake: "worker"}, "2h"},
	} {
		added, problem := s.SubscriptionAdd(ctx, tc.caller, tc.req)
		if problem != nil {
			t.Errorf("%s: %+v", tc.name, problem)
			continue
		}
		sub := listed(added.ID)
		if sub.Wake != worker || sub.Every != tc.every || sub.Room != room || tc.req.Scope == contract.ScopeSession && sub.Session != worker {
			t.Errorf("%s: listed %+v", tc.name, sub)
		}
	}

	for _, tc := range []struct {
		name   string
		caller contract.Principal
		req    contract.SubscriptionAddRequest
		code   contract.ErrorCode
		want   string
	}{
		{"an unknown session name", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "upstream-sync", Every: "1h", Wake: "worker"},
			contract.ErrInvalid, `unknown session "upstream-sync" in this room`},
		{"an unknown wake name", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "worker", Every: "1h", Wake: "upstream-sync"},
			contract.ErrInvalid, `unknown session "upstream-sync" in this room`},
		{"an unknown ID", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: "1h", Wake: "ses_gone"},
			contract.ErrInvalid, `unknown session "ses_gone" in this room`},
		{"a name with no room, from the owner", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "worker", Every: "1h", Wake: string(worker)},
			contract.ErrInvalid, `unknown session "worker"; with no room, name a session by its ses_… ID`},
		{"a session in another room", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Room: other.Room, Session: string(worker), Every: "1h", Wake: string(worker)},
			contract.ErrInvalid, `session "` + string(worker) + `" is not in room ` + string(other.Room)},
		{"seconds with no unit", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "worker", Every: "3600", Wake: "worker"},
			contract.ErrInvalid, `every "3600" is not a duration; give a string such as "1h", "15m" or "90s"`},
		{"under a minute", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: "worker", Every: "30s", Wake: "worker"},
			contract.ErrInvalid, `every "30s" is under a minute; a schedule fires at most once a minute, so every is at least "1m"`},
		{"no session", self, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Every: "1h", Wake: "worker"},
			contract.ErrInvalid, "scope session needs session: a session name or ID"},
		{"no room", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Every: "1h", Wake: "worker"},
			contract.ErrInvalid, "scope room needs room"},
		{"an unknown room", ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: "room_gone", Every: "1h", Wake: "worker"},
			contract.ErrInvalid, `unknown room "room_gone"`},
	} {
		_, problem := s.SubscriptionAdd(ctx, tc.caller, tc.req)
		if problem == nil || problem.Code != tc.code || problem.Message != tc.want {
			t.Errorf("%s: %+v", tc.name, problem)
		}
	}

	for name, call := range map[string]func() *contract.Error{
		"remove": func() *contract.Error {
			_, problem := s.SubscriptionRemove(ctx, ownerCaller, contract.SubscriptionRemoveRequest{ID: "sub_gone"})
			return problem
		},
		"enable": func() *contract.Error {
			_, problem := s.SubscriptionSetEnabled(ctx, ownerCaller, contract.SubscriptionSetEnabledRequest{ID: "sub_gone", Enabled: true})
			return problem
		},
	} {
		if problem := call(); problem == nil || problem.Code != contract.ErrNotFound || problem.Message != `unknown subscription "sub_gone"` || strings.Contains(problem.Message, "sql") {
			t.Errorf("%s: %+v", name, problem)
		}
	}
}
