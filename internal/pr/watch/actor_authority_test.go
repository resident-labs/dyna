package watch

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/subagent"
)

func checkWatchActorAuthority(t *testing.T, action string, authorAllowed bool, call func(*Service, authority.Actor) error) {
	t.Helper()
	s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}}
	// A successful transfer need not route a real notice to test authorization.
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) { return n.ID, nil }
	for _, tc := range []struct {
		name    string
		actor   authority.Actor
		allowed bool
	}{
		{"owner", authority.Actor{Role: authority.Owner}, true},
		{"owner with ID", authority.Actor{Role: authority.Owner, Session: "owner-id"}, true},
		{"root", authority.Actor{Role: authority.Root, Session: "root-id"}, true},
		{"author", authority.Actor{Role: authority.Session, Session: "author"}, authorAllowed},
		{"unrelated", authority.Actor{Role: authority.Session, Session: "other"}, false},
		{"empty session", authority.Actor{Role: authority.Session}, false},
		{"worker with author ID", authority.Actor{Role: authority.Worker, Session: "author"}, false},
		{"empty worker", authority.Actor{Role: authority.Worker}, false},
		{"unknown with author ID", authority.Actor{Session: "author"}, false},
		{"unknown", authority.Actor{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Record{Key: "github.com/owner/repo#1", Host: "github.com", Repo: "owner/repo", Number: 1, Kind: "github", RoomID: "room", Author: "author", State: "open", Watching: true, Findings: []Finding{{ID: "f1"}}}
			if action == "git pr transfer" {
				// Role transfer also supports local changes; checkout discovery is unrelated here.
				r.Kind = "local"
			}
			if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
				t.Fatal(err)
			}
			before, err := s.Store.Get(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			err = call(s, tc.actor)
			if tc.allowed {
				// Draft calls stop at the room boundary after authorization.
				if action == "git pr hold" || action == "git pr release" {
					if err == nil || err.Error() != "a PR hold or release needs the room store" {
						t.Fatalf("want room validation after authorization, got %v", err)
					}
				} else if err != nil {
					t.Fatalf("allowed actor refused: %v", err)
				}
				if action == "git change reply" || action == "ResolveFinding" {
					after, err := s.Store.Get(r.Key)
					if err != nil {
						t.Fatal(err)
					}
					wantAuthor := ""
					if tc.actor.Role == authority.Session {
						wantAuthor = tc.actor.Session
					}
					if got := after.Findings[0].Replies[0].Author; got != wantAuthor {
						t.Fatalf("reply author = %q, want %q", got, wantAuthor)
					}
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), action+" is ") {
				t.Fatalf("want formatted %s authority refusal, got %v", action, err)
			}
			after, err := s.Store.Get(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("refused action changed the record")
			}
		})
	}
}

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
	for _, role := range []authority.Role{authority.Worker, authority.Steward, ""} {
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
