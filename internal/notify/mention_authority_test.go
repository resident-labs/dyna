package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/subagent"
)

// A denied authority rule must stop routing even for an external root.
func TestVoiceCallAuthorityDenial(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	saved := authority.Rules
	authority.Rules = append([]authority.Rule(nil), saved...)
	t.Cleanup(func() { authority.Rules = saved })
	const denial = "test authority denial"
	for i := range authority.Rules {
		if authority.Rules[i].Action == "post @voice-call" {
			authority.Rules[i].Instead = denial
			authority.Rules[i].Allowed = nil
			authority.Rules[i].Relations = nil
		}
	}
	_, err := e.svc.Preview(Sender{Kind: SenderOwner, Room: rec.Name}, []string{"@voice-call"})
	if err == nil || !strings.Contains(err.Error(), denial) {
		t.Fatalf("Preview() error = %v, want %q", err, denial)
	}
}

// Both root forms may call the owner. Other senders cannot borrow the
// engine process's identity to gain that permission.
func TestVoiceCallSenderAuthority(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	session := subagent.Info{Meta: subagent.Meta{ID: "sender", Room: rec.Name, RoomID: rec.ID}}
	root := session
	root.Role = subagent.RoleRoot
	for _, tc := range []struct {
		name    string
		sender  Sender
		allowed bool
	}{
		{"owner", Sender{Kind: SenderOwner, Room: rec.Name, Human: true}, false},
		{"external root", Sender{Kind: SenderOwner, Room: rec.Name}, true},
		{"root session", Sender{Kind: SenderSession, Room: rec.Name, Session: root}, true},
		{"session", Sender{Kind: SenderSession, Room: rec.Name, Session: session}, false},
		{"script", Sender{Kind: SenderScript, Room: rec.Name}, false},
		{"worker", Sender{Kind: SenderWorker, Room: rec.Name}, false},
		{"voice", Sender{Kind: SenderVoice, Room: rec.Name}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"@voice-call", "@" + rec.Name + ":voice-call"} {
				got, err := e.svc.Preview(tc.sender, []string{token})
				if !tc.allowed {
					if err == nil || !strings.HasPrefix(err.Error(), "post @voice-call is root only;") {
						t.Fatalf("%s: error = %v, want authority denial", token, err)
					}
					continue
				}
				if err != nil || len(got.Recipients) != 1 || got.Recipients[0] != rec.Name+":voice-call" {
					t.Fatalf("%s: recipients = %v, error = %v", token, got.Recipients, err)
				}
			}
		})
	}
}
