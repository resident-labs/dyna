package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/room"
)

// signedClaudeAccount creates a Claude Code account with a saved login, so a
// room selection can be made for it.
func signedClaudeAccount(t *testing.T, accounts *harnessruntime.Accounts) string {
	t.Helper()
	n, err := accounts.Create("claude-code", "Work")
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"work-access","refreshToken":"work-refresh","expiresAt":1999999999999,"scopes":["user:inference"]}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"aaaaaaaa-bbbb-cccc-dddd-000000000002","emailAddress":"work@example.test"}}`,
	} {
		if err := os.WriteFile(filepath.Join(n.Home, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := accounts.Capture(context.Background(), n.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	return n.ID
}

// A room choice changes that room's future turns, so a session may choose only
// for its own room. The owner may choose for any room.
func TestAccountsUseChecksTheCallersRoom(t *testing.T) {
	s := testServices(t, false)
	own, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "own"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	id := signedClaudeAccount(t, s.runtime.Accounts)
	selections := &store.Store{Root: s.runtime.Accounts.Root}
	activeIn := func(roomID string) string {
		t.Helper()
		found, err := selections.Active(store.ProviderAnthropic, "claude-code", roomID)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	session := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "ses_test", Room: own.ID})
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})

	if _, err := s.accountsUse(session, id, foreign.Name); err == nil || !strings.Contains(err.Error(), "own room") {
		t.Fatalf("a session in another room was accepted: %v", err)
	}
	if activeIn(foreign.ID) != "" {
		t.Fatal("the refused request changed the foreign room")
	}
	if _, err := s.accountsUse(session, id, own.Name); err != nil {
		t.Fatalf("a session was refused in its own room: %v", err)
	}
	if activeIn(own.ID) != id {
		t.Fatal("the own-room request did not change the own room")
	}
	if _, err := s.accountsUse(owner, id, foreign.Name); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
	if activeIn(foreign.ID) != id {
		t.Fatal("the owner request did not change the foreign room")
	}
}
