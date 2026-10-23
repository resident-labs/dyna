package library

import (
	"errors"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/store/storetest"
)

func TestLibraryMutationsUseExplicitActor(t *testing.T) {
	// An engine's owner process must not replace a session sender's identity.
	storetest.ClearStamp(t)
	data := t.TempDir()
	states := librarianstate.Store{DataDir: data}
	if _, err := states.Update("repo", true, func(st *librarianstate.State) error {
		st.Session, st.RoomID = "librarian", "room"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actors := []struct {
		name    string
		actor   authority.Actor
		allowed bool
	}{
		{"owner", authority.Actor{Role: authority.Owner}, true},
		{"root", authority.Actor{Role: authority.Root}, true},
		{"librarian", authority.Actor{Role: authority.Session, Session: "librarian"}, true},
		{"other-session", authority.Actor{Role: authority.Session, Session: "other"}, false},
		{"empty-session", authority.Actor{Role: authority.Session}, false},
		{"worker-with-librarian-id", authority.Actor{Role: authority.Worker, Session: "librarian"}, false},
		{"unknown-with-librarian-id", authority.Actor{Session: "librarian"}, false},
		{"no-actor", authority.Actor{}, false},
	}
	owner := Store{DataDir: data, Actor: authority.Actor{Role: authority.Owner}}
	for _, tc := range actors {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.name
			if _, err := owner.Add("repo", entry(id, "")); err != nil {
				t.Fatal(err)
			}
			s := Store{DataDir: data, Actor: tc.actor}
			_, err := s.Add("repo", entry(id+"-new", ""))
			assertLibraryAuthority(t, "library add", err, tc.allowed)
			_, savedErr := owner.Get("repo", id+"-new")
			if tc.allowed && savedErr != nil {
				t.Errorf("allowed add was not saved: %v", savedErr)
			}
			if !tc.allowed && !errors.Is(savedErr, os.ErrNotExist) {
				t.Errorf("denied add changed entries: %v", savedErr)
			}
			called := false
			reached := errors.New("update callback reached")
			_, err = s.Update("repo", id, func(*Entry) error { called = true; return reached })
			if tc.allowed {
				if !called || !errors.Is(err, reached) {
					t.Fatalf("update callback=%v, err=%v", called, err)
				}
			} else {
				assertLibraryAuthority(t, "library update", err, false)
				if called {
					t.Error("denied update ran callback")
				}
			}
			_, err = s.Retire("repo", id, "")
			assertLibraryAuthority(t, "library retire", err, tc.allowed)
			got, err := owner.Get("repo", id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Retired != tc.allowed {
				t.Fatalf("retired=%v; allowed=%v", got.Retired, tc.allowed)
			}
			if _, err := s.Get("repo", id); err != nil {
				t.Fatalf("read requires edit authority: %v", err)
			}
			if _, err := s.List("repo", true); err != nil {
				t.Fatalf("list requires edit authority: %v", err)
			}
		})
	}
}

func TestLibraryLibrarianRelationBelongsToRepository(t *testing.T) {
	data := t.TempDir()
	states := librarianstate.Store{DataDir: data}
	if _, err := states.Update("other", true, func(st *librarianstate.State) error { st.Session = "librarian"; return nil }); err != nil {
		t.Fatal(err)
	}
	s := Store{DataDir: data, Actor: authority.Actor{Role: authority.Session, Session: "librarian"}}
	_, err := s.Add("repo", entry("feature", ""))
	assertLibraryAuthority(t, "library add", err, false)
	rows, err := s.List("repo", true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("denied add changed map: %+v, %v", rows, err)
	}
}

func assertLibraryAuthority(t *testing.T, action string, err error, allowed bool) {
	t.Helper()
	if allowed {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil || !strings.HasPrefix(err.Error(), action+" is ") {
		t.Errorf("%s refusal: %v", action, err)
	}
}

func TestLibraryMutationReloadsSavedLibrarian(t *testing.T) {
	data := t.TempDir()
	states := librarianstate.Store{DataDir: data}
	setSession := func(session string) {
		t.Helper()
		if _, err := states.Update("repo", true, func(st *librarianstate.State) error { st.Session = session; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	setSession("librarian")
	s := Store{DataDir: data, Actor: authority.Actor{Role: authority.Session, Session: "librarian"}}
	if _, err := s.Add("repo", entry("feature", "")); err != nil {
		t.Fatal(err)
	}
	setSession("replacement")
	_, err := s.Update("repo", "feature", func(*Entry) error { t.Fatal("former librarian ran mutation callback"); return nil })
	assertLibraryAuthority(t, "library update", err, false)
	s.Actor.Session = "replacement"
	if _, err := s.Update("repo", "feature", func(e *Entry) error { e.Summary = "Updated."; return nil }); err != nil {
		t.Fatal(err)
	}
}
