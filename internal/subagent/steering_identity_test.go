package subagent

import (
	"reflect"
	"testing"
)

func TestDeliveryDoesNotConsumeEqualTextOtherPost(t *testing.T) {
	zero := int64(0)
	for _, tc := range []struct {
		name string
		note Steer
		want []bool
	}{
		{"exact_post", Steer{ID: "msg_a", TS: 7, Message: "same", SourceOffset: &zero}, []bool{true, false, false}},
		{"identity_survives_frame_change", Steer{ID: "msg_a", TS: 8, Message: "new frame"}, []bool{true, false, false}},
		{"base_runner_receipt_without_id", Steer{TS: 7, Message: "same"}, []bool{true, true, true}},
		{"unknown_post", Steer{ID: "msg_unknown", TS: 7, Message: "same"}, []bool{false, false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := turnStore(t)
			if err := s.writeTurn(id, Turn{N: 1, Steering: []Steer{
				{ID: "msg_a", TS: 7, Message: "same"},
				{ID: "msg_b", TS: 7, Message: "same"},
				{TS: 7, Message: "same"},
			}}); err != nil {
				t.Fatal(err)
			}
			m := &Manager{Store: s}
			if err := m.noteTurn(t.Context(), id, TurnNote{Turn: 1, Delivered: &tc.note}); err != nil {
				t.Fatal(err)
			}
			got, err := s.ReadTurn(id, 1)
			if err != nil {
				t.Fatal(err)
			}
			var delivered []bool
			for i, st := range got.Steering {
				delivered = append(delivered, st.Delivered)
				if tc.want[i] && tc.note.SourceOffset != nil {
					if st.SourceOffset == nil || *st.SourceOffset != 0 {
						t.Fatalf("post %d lost its known zero boundary: %v", i, st.SourceOffset)
					}
				} else if st.SourceOffset != nil {
					t.Fatalf("post %d gained an unknown boundary: %v", i, st.SourceOffset)
				}
			}
			if !reflect.DeepEqual(delivered, tc.want) {
				t.Fatalf("delivered = %v; want %v", delivered, tc.want)
			}
		})
	}
}
