package roomsvc

import (
	"testing"

	"dyna-agent/internal/room"
)

// A reply target comes from the stored sender identity, so a session in
// another room is named as itself and no address prefix decides a recipient.
func TestReplyTargetReadsStoredSender(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  room.Message
		want *ReplyTarget
	}{
		{
			name: "session in this room",
			msg:  room.Message{From: "alpha:worker", SessionIDs: map[string]string{"alpha:worker": "ses_1"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_1", Address: "alpha:worker"},
		},
		{
			name: "session in another room",
			msg:  room.Message{From: "beta:other", SessionIDs: map[string]string{"beta:other": "ses_2"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_2", Address: "beta:other"},
		},
		{
			name: "root seat",
			msg:  room.Message{From: "alpha:root"},
			want: &ReplyTarget{Kind: ReplyRoom, Address: "alpha"},
		},
		{
			name: "engine post",
			msg:  room.Message{From: "alpha"},
			want: &ReplyTarget{Kind: ReplyRoom, Address: "alpha"},
		},
		{
			name: "owner's own post",
			msg:  room.Message{From: "alpha", Human: true},
		},
		{
			name: "workflow worker",
			msg:  room.Message{From: "run:wf_1/agent:1"},
			want: &ReplyTarget{Kind: ReplyRoom, Address: "alpha"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replyTarget(tc.msg, "alpha")
			if tc.want == nil {
				if got != nil {
					t.Fatalf("reply target = %+v, want none", got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("reply target = %+v, want %+v", got, tc.want)
			}
		})
	}
}
