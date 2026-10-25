package roomsvc

import (
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// replyTarget names the stored sender of a post, so a session in another room
// is named as itself and no address prefix decides a recipient.
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
			msg:  room.Message{From: "beta:other", SessionIDs: map[string]string{"beta:other": "ses_beta"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_beta", Address: "beta:other"},
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
			book := newAvatarBook(memberSessions(), "room_alpha")
			got := replyTarget(tc.msg, "alpha", book)
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

// A sender that moved to another name, or another room, answers at the address
// it holds now. An address another session took names nobody: a reply must not
// reach the session that reused the name.
func TestReplyTargetFollowsTheSessionNotItsStoredAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  room.Message
		want *ReplyTarget
	}{
		{
			name: "renamed in another room",
			msg:  room.Message{From: "beta:old", SessionIDs: map[string]string{"beta:old": "ses_beta"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_beta", Address: "beta:other"},
		},
		{
			name: "name reused after a close",
			msg:  room.Message{From: "alpha:lead", SessionIDs: map[string]string{"alpha:lead": "ses_closed_lead"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_closed_lead"},
		},
		{
			// The older live session renamed to the name a newer session closed
			// with, so a mention of that name reaches the older session.
			name: "live session took a closed session's name",
			msg:  room.Message{From: "beta:builder", SessionIDs: map[string]string{"beta:builder": "ses_new_closed"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_new_closed"},
		},
		{
			name: "session gone from the store",
			msg:  room.Message{From: "alpha:worker", SessionIDs: map[string]string{"alpha:worker": "ses_retired"}},
			want: &ReplyTarget{Kind: ReplySession, ID: "ses_retired"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := newAvatarBook(memberSessions(), "room_alpha")
			got := replyTarget(tc.msg, "alpha", book)
			if got == nil || *got != *tc.want {
				t.Fatalf("reply target = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// memberSessions is one room with a live session, a session of another room
// that changed its name, a closed session whose name a newer session took, and
// an older live session that took the name of a newer closed one.
func memberSessions() []subagent.Info {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	session := func(id, roomName, name string, created time.Time, closed bool) subagent.Info {
		status := subagent.StatusIdle
		if closed {
			status = subagent.StatusClosed
		}
		return subagent.Info{Meta: subagent.Meta{ID: id, RoomID: "room_" + roomName, Room: roomName, Name: name, Created: created}, Status: status}
	}
	return []subagent.Info{
		session("ses_1", "alpha", "worker", at, false),
		session("ses_beta", "beta", "other", at.Add(time.Minute), false),
		session("ses_closed_lead", "alpha", "lead", at.Add(2*time.Minute), true),
		session("ses_lead", "alpha", "lead", at.Add(3*time.Minute), false),
		// The newer session closed with this name; the older one holds it now.
		session("ses_new_closed", "beta", "builder", at.Add(4*time.Minute), true),
		session("ses_old", "beta", "builder", at, false),
	}
}
