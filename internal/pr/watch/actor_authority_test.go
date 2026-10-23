package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/subagent"
)

func TestTypedRoomPolicyAuthority(t *testing.T) {
	checkTypedRoomAuthority(t, "SetRoomChangePolicy", func(s *Service, a authority.Actor) error { return s.SetRoomChangePolicy("", a, ChangePolicy{}) })
}

func TestTypedRoomBudgetAuthority(t *testing.T) {
	checkTypedRoomAuthority(t, "SetRoomCIBudget", func(s *Service, a authority.Actor) error { return s.SetRoomCIBudget("", a, ChangePolicy{}) })
}

func checkTypedRoomAuthority(t *testing.T, action string, call func(*Service, authority.Actor) error) {
	t.Helper()
	s := &Service{}
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, ""} {
		for _, id := range []string{"", "session"} {
			t.Run(string(role)+"/"+id, func(t *testing.T) {
				err := call(s, authority.Actor{Role: role, Session: id})
				if role == authority.Owner || role == authority.Root {
					if err == nil || err.Error() != "room is required" {
						t.Fatalf("want room validation, got %v", err)
					}
				} else if err == nil || !strings.HasPrefix(err.Error(), action+" is owner or root only; ") {
					t.Fatalf("want formatted authority refusal, got %v", err)
				}
			})
		}
	}
}

func TestOpenAndTrackRejectUnauthorizedActorsBeforeBackend(t *testing.T) {
	for _, role := range []authority.Role{authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			actor := authority.Actor{Role: role, Session: "author"}
			var s *Service
			for name, call := range map[string]func() error{
				"track": func() error {
					_, _, err := s.Track(context.Background(), actor, Ref{}, subagent.Info{}, "")
					return err
				},
				"track with policy": func() error {
					_, _, err := s.TrackWithPolicy(context.Background(), actor, Ref{}, subagent.Info{}, "", PolicyOverride{Review: "off", Reason: "bypass"}, "")
					return err
				},
				"open": func() error {
					_, _, err := s.OpenChange(context.Background(), actor, subagent.Info{}, OpenChangeOptions{})
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					if err := call(); err == nil || !strings.Contains(err.Error(), "only") {
						t.Fatalf("expected authority refusal before backend access: %v", err)
					}
				})
			}
		})
	}
}
