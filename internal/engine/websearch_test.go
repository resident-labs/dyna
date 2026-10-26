package engine

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// searchRepo makes a repository checkout whose origin names remote, so
// RepoIdentity reads the same identity the account rules use.
func searchRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// A search reads its repository from the session's own record, so a session in
// one repository cannot spend a Brave key the owner limited to another, even
// when it asks through the engine's own socket.
func TestWebSearchKeepsTheSessionsRepositoryScope(t *testing.T) {
	s := testServices(t, false)
	const work, personal = "github.com/acme/work", "github.com/acme/personal"
	personalDir := searchRepo(t, "git@github.com:acme/personal.git")
	recordtest.Write(t, s.rooms.Notify.Sessions.Store.Root, "ses_b", subagent.Meta{
		ID: "ses_b", Name: "b", RoomID: "room_1", Room: "dyna", Dir: personalDir, Profile: "deepseek", Turns: 1,
	})
	account, err := s.runtime.AddProviderKey(t.Context(), "pi", accountstore.ProviderBrave, "brave", "brave-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runtime.Accounts.SetRepos(account.ID, []string{work}); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime.Accounts.SelectInScope(account, "room_1"); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "ses_b", Room: "room_1"})

	room, repo := s.searchScope(ctx)
	if room != "room_1" || repo != personal {
		t.Fatalf("scope = %q/%q, want the session's own room and repository", room, repo)
	}
	if key, err := s.runtime.Accounts.PiProviderKey(ctx, accountstore.ProviderBrave, room, repo); err == nil {
		t.Fatalf("the search read %q from an account limited to %s", key, work)
	}
	// The same key still answers in the repository the owner allowed.
	if key, err := s.runtime.Accounts.PiProviderKey(ctx, accountstore.ProviderBrave, room, work); err != nil || key != "brave-key" {
		t.Fatalf("the allowed repository read %q, %v; want the saved key", key, err)
	}
}

// A search whose caller has no stored session names no repository, so a limited
// account cannot answer it.
func TestWebSearchWithNoStoredSessionNamesNoRepository(t *testing.T) {
	s := testServices(t, false)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	room, repo := s.searchScope(ctx)
	if room != "" || repo != "" {
		t.Fatalf("scope = %q/%q, want no room and no repository", room, repo)
	}
}

// With no sign-in and no key saved at all, the engine names every provider in
// the owner's order in one line and never reaches the network.
func TestWebSearchTriesEveryProviderInTheOwnerOrder(t *testing.T) {
	calls := testCalls(t, false)
	// The call is local-only, so a session reaches it through the engine's own
	// socket, as the shell MCP process does.
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	_, err := calls["WebSearch"](ctx, []json.RawMessage{json.RawMessage(`{"query":"go language","limit":3}`)})
	if err == nil {
		t.Fatal("a search with no credential answered")
	}
	message := err.Error()
	if strings.Contains(message, "\n") {
		t.Fatalf("error spans more than one line: %q", message)
	}
	// The sign-in providers answer first, because they cost the owner no
	// search API key; the key providers follow in the order the owner named.
	order := []string{"chatgpt", "claude", "muse", "kimi", "zai", "xai", "brave", "exa", "tavily"}
	at := 0
	for _, name := range order {
		reason := name + " is not signed in"
		next := strings.Index(message[at:], reason)
		if next < 0 {
			t.Fatalf("error = %q, want %q after position %d", message, reason, at)
		}
		at += next + len(reason)
	}
}
