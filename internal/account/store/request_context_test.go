package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	statewrite "dyna-agent/internal/state"
)

func TestUpdateContextRecordsOnlyTheCallbackSave(t *testing.T) {
	for _, maintenance := range []string{"none", "cleanup"} {
		t.Run(maintenance, func(t *testing.T) {
			s := registryStore(t)
			meta := saveAccount(t, s, "fixture-token")
			if err := statewrite.MigrateRequests(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			if maintenance == "cleanup" {
				if err := s.Update(func(st *State) error {
					st.OpenCodeProvidersCleaned = false
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			db, err := s.db()
			if err != nil {
				t.Fatal(err)
			}
			w := statewrite.For(db)
			request := func(fingerprint string) context.Context {
				return statewrite.ForRequestCall(t.Context(), "quota-request", "ObserveMuseQuota", []byte(fingerprint))
			}
			abort := errors.New("observation rejected")
			err = s.UpdateContext(request("original"), func(st *State) error {
				if _, done, err := w.RequestDone(t.Context(), "quota-request"); err != nil || done {
					t.Fatalf("maintenance consumed the request: done=%v, err=%v", done, err)
				}
				st.Find(meta.ID).Label = "must not commit"
				return abort
			})
			if !errors.Is(err, abort) {
				t.Fatalf("failed callback = %v", err)
			}
			if _, done, err := w.RequestDone(t.Context(), "quota-request"); err != nil || done {
				t.Fatalf("failed callback left a receipt: done=%v, err=%v", done, err)
			}
			st, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !st.OpenCodeProvidersCleaned || st.Find(meta.ID).Label == "must not commit" {
				t.Fatal("maintenance or callback rollback changed")
			}
			update := func(st *State) error {
				entry := st.Find(meta.ID)
				if entry.Observation == nil {
					entry.Observation = &Observation{}
				}
				entry.Observation.Failures++
				return nil
			}
			if err := s.UpdateContext(request("original"), update); err != nil {
				t.Fatal(err)
			}
			if result, done, err := w.RequestDone(request("original"), "quota-request"); err != nil || !done || result != nil {
				t.Fatalf("callback save lacks its mutation receipt: done=%v, result=%s, err=%v", done, result, err)
			}
			if err := s.UpdateContext(request("original"), update); !errors.Is(err, statewrite.ErrRepeated) {
				t.Fatalf("repeated mutation = %v", err)
			}
			if err := s.UpdateContext(request("changed"), update); !errors.Is(err, statewrite.ErrRequestConflict) {
				t.Fatalf("changed arguments = %v", err)
			}
			st, err = s.Read()
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Find(meta.ID).Observation.Failures; got != 1 {
				t.Fatalf("observation applied %d times", got)
			}
		})
	}
}

func TestUpdateContextCancellationDoesNotCommit(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "fixture-token")
	if err := statewrite.MigrateRequests(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(statewrite.ForRequestCall(t.Context(), "cancelled-quota", "ObserveMuseQuota", []byte("original")))
	defer cancel()
	err := s.UpdateContext(ctx, func(st *State) error {
		st.Find(meta.ID).Label = "must not commit"
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("cancelled callback save succeeded")
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, done, err := statewrite.For(db).RequestDone(t.Context(), "cancelled-quota"); err != nil || done {
		t.Fatalf("cancelled save left a receipt: done=%v, err=%v", done, err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Find(meta.ID).Label != meta.Label {
		t.Fatal("cancelled save changed the account")
	}
}
