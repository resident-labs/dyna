package room

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// ownerCtx acts for the owner, which most issue tests need now that an issue
// change checks the caller on ctx instead of the address it credits.
func ownerCtx() context.Context {
	return authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
}

// sessionCtx acts for a session in roomID, which may not close or link issues.
func sessionCtx(roomID string) context.Context {
	return authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: "ses-test", Room: roomID})
}

// Issue triage checks the caller that the engine verified, never the address
// that the change is credited to. A session that types the room's own name, or
// the root seat's address, gains nothing by it.
func TestIssueTriageUsesCallerAuthority(t *testing.T) {
	s, rec, _ := pinFixture(t)
	item, err := s.FileIssue(rec.ID, IssueInput{Title: "Triage", Note: "", File: "", Line: 0, Filer: rec.Name})
	if err != nil {
		t.Fatal(err)
	}
	actorCtx := func(a authority.Actor) context.Context { return authority.WithActor(context.Background(), a) }
	session := sessionCtx(rec.ID)
	otherSession := actorCtx(authority.Actor{Role: authority.Session, Session: "ses-other", Room: "room-other"})
	reached := errors.New("triage callback reached")
	change := func(*Issue) error { return reached }
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		by      string
		allowed bool
		refusal string
	}{
		{name: "session spoofing the room name", ctx: session, by: rec.Name, refusal: "ChangeIssue is "},
		{name: "session spoofing the root seat", ctx: session, by: rec.Name + ":root", refusal: "ChangeIssue is "},
		{name: "session crediting another room", ctx: otherSession, by: "other", refusal: "only in its own room"},
		{name: "owner", ctx: ownerCtx(), by: rec.Name, allowed: true},
		{name: "owner crediting a session", ctx: ownerCtx(), by: rec.Name + ":agent", allowed: true},
		{name: "root seat", ctx: actorCtx(authority.EngineRoomActor(rec.ID)), by: rec.Name + ":root", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ChangeIssue(tc.ctx, rec.ID, item.ID, tc.by, change)
			if tc.allowed {
				if !errors.Is(err, reached) {
					t.Fatalf("allowed caller refused: %v", err)
				}
				return
			}
			if errors.Is(err, reached) || err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("refusal = %v, want one that says %q", err, tc.refusal)
			}
		})
	}
}
