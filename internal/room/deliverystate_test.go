package room

import (
	"context"
	"testing"

	"dyna-agent/internal/hub"
)

// The delivery-state writes keep the rules the session_queues blob gave: a
// claim names one turn, a cancellation only drops an unclaimed post, a
// dismissal drops a pending one, and a cancellation or dismissal is final.
func TestDeliveryStateKeepsTheClaimRules(t *testing.T) {
	const session = "session-a"
	setup := func(t *testing.T) (*Store, DeliverySet, Message) {
		t.Helper()
		s := recordsStore(t)
		rec, err := s.Create(CreateInput{Name: "alpha"})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := s.Post(rec.ID, PostInput{Wake: true, From: rec.Name, To: []string{"alpha:bea"}, SessionIDs: map[string]string{"alpha:bea": session}, Text: "for bea"})
		if err != nil {
			t.Fatal(err)
		}
		return s, DeliverySet{RoomID: rec.ID, Keys: DeliveryKeys(session, "alpha:bea")}, msg
	}
	row := func(t *testing.T, s *Store, set DeliverySet, msg Message) Delivery {
		t.Helper()
		rows, err := s.SessionDeliveries(set.RoomID, set.Keys, []string{msg.ID})
		if err != nil {
			t.Fatal(err)
		}
		got, ok := rows[msg.ID]
		if !ok {
			t.Fatalf("no delivery row for post %s", msg.ID)
		}
		return got
	}
	claim := func(t *testing.T, s *Store, set DeliverySet, msg Message, turn int) []string {
		t.Helper()
		claimed, err := s.ClaimDeliveries(context.Background(), hub.Event{}, set, []string{msg.ID}, turn)
		if err != nil {
			t.Fatal(err)
		}
		return claimed
	}
	cases := []struct {
		name string
		run  func(t *testing.T, s *Store, set DeliverySet, msg Message)
	}{{
		name: "a claim names its turn",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			if claimed := claim(t, s, set, msg, 1); len(claimed) != 1 {
				t.Fatalf("claimed %v", claimed)
			}
			if got := row(t, s, set, msg); got.State != DeliveryClaimed || got.ClaimedTurn != 1 {
				t.Fatalf("row = %+v", got)
			}
		},
	}, {
		name: "the same turn claiming again keeps one claim",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			claim(t, s, set, msg, 1)
			if claimed := claim(t, s, set, msg, 1); len(claimed) != 1 {
				t.Fatalf("repeat claim = %v, want the post again", claimed)
			}
			if got := row(t, s, set, msg); got.State != DeliveryClaimed || got.ClaimedTurn != 1 {
				t.Fatalf("row = %+v", got)
			}
		},
	}, {
		name: "a post has one claiming turn at a time",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			claim(t, s, set, msg, 1)
			// Turn 1 is finished: a session runs one turn at a time, so this is
			// the claim a recovery takes over, never a second live owner.
			claim(t, s, set, msg, 2)
			if got := row(t, s, set, msg); got.ClaimedTurn != 2 {
				t.Fatalf("row = %+v, want the one claiming turn to be 2", got)
			}
		},
	}, {
		name: "a cancel drops only a queued delivery",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			cancelled, err := s.CancelDelivery(context.Background(), hub.Event{}, set, msg.ID)
			if err != nil || !cancelled {
				t.Fatalf("cancel = %v, %v", cancelled, err)
			}
			if got := row(t, s, set, msg); got.State != DeliveryCancelled {
				t.Fatalf("row = %+v", got)
			}
			if claimed := claim(t, s, set, msg, 1); len(claimed) != 0 {
				t.Fatalf("a cancelled post was claimed: %v", claimed)
			}
			if got := row(t, s, set, msg); got.State != DeliveryCancelled {
				t.Fatalf("row = %+v", got)
			}
			if cancelled, err := s.CancelDelivery(context.Background(), hub.Event{}, set, msg.ID); err != nil || cancelled {
				t.Fatalf("a second cancel = %v, %v; want nothing to cancel", cancelled, err)
			}
		},
	}, {
		name: "a cancel fails once a turn claimed the post",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			claim(t, s, set, msg, 1)
			cancelled, err := s.CancelDelivery(context.Background(), hub.Event{}, set, msg.ID)
			if err != nil || cancelled {
				t.Fatalf("cancel after claim = %v, %v", cancelled, err)
			}
			if got := row(t, s, set, msg); got.State != DeliveryClaimed {
				t.Fatalf("row = %+v", got)
			}
		},
	}, {
		name: "a dismissal drops a claimed delivery and keeps it dropped",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			claim(t, s, set, msg, 1)
			dismissed, err := s.DismissDelivery(context.Background(), hub.Event{}, set, msg.ID)
			if err != nil || !dismissed {
				t.Fatalf("dismiss = %v, %v", dismissed, err)
			}
			if got := row(t, s, set, msg); got.State != DeliveryDismissed {
				t.Fatalf("row = %+v", got)
			}
			if claimed := claim(t, s, set, msg, 2); len(claimed) != 0 {
				t.Fatalf("a dismissed post was claimed: %v", claimed)
			}
			if marked, err := s.MarkDelivered(context.Background(), set, []string{msg.ID}); err != nil || len(marked) != 0 {
				t.Fatalf("a dismissed post was marked delivered: %v, %v", marked, err)
			}
			if got := row(t, s, set, msg); got.State != DeliveryDismissed {
				t.Fatalf("row = %+v", got)
			}
		},
	}, {
		name: "delivering a claimed post records it",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			claim(t, s, set, msg, 1)
			if marked, err := s.MarkDelivered(context.Background(), set, []string{msg.ID}); err != nil || len(marked) != 1 {
				t.Fatalf("mark delivered = %v, %v", marked, err)
			}
			if got := row(t, s, set, msg); got.State != DeliveryDelivered || got.ClaimedTurn != 1 {
				t.Fatalf("row = %+v", got)
			}
		},
	}, {
		name: "a claim leaves another session's row alone",
		run: func(t *testing.T, s *Store, set DeliverySet, msg Message) {
			other := DeliverySet{RoomID: set.RoomID, Keys: DeliveryKeys("session-b", "alpha:other")}
			claim(t, s, other, msg, 1)
			if got := row(t, s, set, msg); got.State != DeliveryQueued {
				t.Fatalf("row = %+v", got)
			}
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, set, msg := setup(t)
			tc.run(t, s, set, msg)
		})
	}
}
