package roomsvc

import (
	"dyna-agent/internal/authority"
	"testing"
)

func TestSenderMuteRequiresRecipientRoot(t *testing.T) {
	e := newEnv(t)
	own := e.room("own")
	other := e.room("other")
	for _, tc := range []struct {
		role    authority.Role
		room    string
		allowed bool
	}{
		{authority.Owner, "", true}, {authority.Root, own.ID, true},
		{authority.Root, other.ID, false}, {authority.Session, own.ID, false},
	} {
		ctx := authority.WithActor(e.ctx, authority.Actor{Role: tc.role, Room: tc.room})
		err := e.svc.SetSenderMute(ctx, own.ID, "room", other.ID, 60)
		if (err == nil) != tc.allowed {
			t.Fatalf("role %s room %s: %v", tc.role, tc.room, err)
		}
	}
}
