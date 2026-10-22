package watch

import (
	"context"
	"fmt"
	"testing"
)

func TestIdleNotificationsScaleWithHistory(t *testing.T) {
	for _, method := range []struct {
		name string
		run  func(*Service) error
	}{
		{"Tick", func(s *Service) error { _, err := s.Tick(context.Background()); return err }},
		{"dispatchAll", func(s *Service) error { return s.dispatchAll(context.Background()) }},
	} {
		t.Run(method.name, func(t *testing.T) {
			allocations := func(count int) float64 {
				s := &Service{Store: &Store{Root: t.TempDir()}}
				if err := s.Store.update(func(st *State) error {
					for i := 0; i < count; i++ {
						key := fmt.Sprintf("historical-%d", i)
						st.Records[key] = &Record{Key: key, State: "closed", Notices: []Notice{
							{ID: "delivered", Done: true},
							{ID: "superseded", SupersededBy: "delivered"},
						}}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return testing.AllocsPerRun(3, func() {
					if err := method.run(s); err != nil {
						t.Fatal(err)
					}
				})
			}
			// Ten times the history should cost roughly ten times the allocations.
			// Reloading the full state for each record instead grows quadratically.
			small, large := allocations(10), allocations(100)
			if large > 20*small {
				t.Fatalf("idle allocations grew from %.0f to %.0f for 10 times the history", small, large)
			}
		})
	}
}

func TestDispatchAllDeliversOnlyPendingNotices(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		st.Records["closed"] = &Record{Key: "closed", State: "closed", Notices: []Notice{
			{ID: "delivered", Done: true},
			{ID: "superseded", SupersededBy: "pending"},
			{ID: "pending"},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var delivered []string
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		delivered = append(delivered, n.ID)
		return "post", nil
	}
	if err := s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0] != "pending" {
		t.Fatalf("delivered notices = %v", delivered)
	}
	if err := s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered notices again: %v", delivered)
	}
}

func TestTickSkipsRecordRemovedBetweenRefreshes(t *testing.T) {
	if hasPendingNotices(nil) {
		t.Fatal("missing record has notices")
	}
	e := newEnv(t)
	if err := e.s.Store.update(func(st *State) error {
		st.Records["a-notice"] = &Record{Key: "a-notice", State: "closed", Notices: []Notice{{ID: "pending"}}}
		st.Records["b-refresh"] = &Record{Key: "b-refresh", Host: "github.com", Repo: "owner/repo", Number: 17, State: "open"}
		st.Records["c-removed"] = &Record{Key: "c-removed", State: "closed"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delivered := false
	e.s.Deliver = func(_ context.Context, _ Record, _ Notice) (string, error) {
		delivered = true
		return "post", e.s.Store.update(func(st *State) error { delete(st.Records, "c-removed"); return nil })
	}
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("notice was not delivered")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Records["c-removed"] != nil {
		t.Fatal("removed record returned")
	}
}
