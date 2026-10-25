package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

func modTime(path string) time.Time {
	if info, err := os.Lstat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func signedClaude(t *testing.T, a *Accounts, label, uuid string) NativeAccount {
	t.Helper()
	n, err := a.Create("claude-code", label)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"` + label + `-access","refreshToken":"` + label + `-refresh","expiresAt":1999999999999,"scopes":["user:inference"]}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"` + uuid + `","emailAddress":"` + label + `@example.test"}}`,
	} {
		if err = os.WriteFile(filepath.Join(n.Home, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n, err = a.Capture(context.Background(), n.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	return n
}

// A Claude switch must not wait for running turns. The next turn uses the new
// account's credentials folder, and the running turn's folder stays as it was.
func TestClaudeSwitchDoesNotWaitForRunningTurns(t *testing.T) {
	a := testAccounts(t)
	personal := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	work := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.Switch(context.Background(), personal.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	running, err := a.Env(personal.ID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := a.TurnLease(context.Background(), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	credentials := filepath.Join(running[pinnedSecretKey], ".credentials.json")
	before, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	beforeTime := modTime(credentials)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.Switch(ctx, work.ID, "/fake/native"); err != nil {
		t.Fatal("switch waited for the running turn:", err)
	}
	if active, err := a.Active(accountProvider("claude-code"), "claude-code"); err != nil || active.ID != work.ID {
		t.Fatal("switch did not select the new account", active, err)
	}
	next, err := a.Env(work.ID)
	if err != nil || next[pinnedSecretKey] != work.Home {
		t.Fatal("the next turn does not use the new account's credentials", next[pinnedSecretKey], err)
	}
	after, err := os.ReadFile(credentials)
	if err != nil || !bytes.Equal(before, after) || !modTime(credentials).Equal(beforeTime) {
		t.Fatal("the switch changed the running turn's credentials", err)
	}
}

// Builds before per-account credentials kept the active login in the shared
// runtime home, and their turns may refresh it there. The first pin adopts it.
func TestClaudePinAdoptsNewerSharedCredentials(t *testing.T) {
	a := testAccounts(t)
	n := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	if err := a.transaction(func(*store.State) error { return a.setActive("claude-code", n.ID) }, true); err != nil {
		t.Fatal(err)
	}
	shared, _ := a.HarnessHome("claude-code")
	if err := os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	refreshed := `{"claudeAiOauth":{"accessToken":"refreshed-by-old-turn","refreshToken":"rotated","expiresAt":2099999999999,"scopes":["user:inference"]}}`
	if err := os.WriteFile(filepath.Join(shared, ".credentials.json"), []byte(refreshed), 0600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(shared, ".credentials.json"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := a.prepareTurnPin(context.Background(), n, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(n.Home, ".credentials.json"))
	if err != nil || !bytes.Contains(raw, []byte("refreshed-by-old-turn")) {
		t.Fatal("the pin did not adopt the newer shared credentials", string(raw), err)
	}
}

// invalidGrantClaude stands in for a provider that has rotated the saved
// refresh token: any refresh fails with ErrInvalidGrant.
type invalidGrantClaude struct{ native.Manager }

func (invalidGrantClaude) Refresh(_ context.Context, c native.Captured) (native.Captured, error) {
	return c, native.ErrInvalidGrant
}
func (invalidGrantClaude) RefreshActive(_ context.Context, c native.Captured, _ func(native.Captured) error) (native.Captured, error) {
	return c, native.ErrInvalidGrant
}
func (m invalidGrantClaude) ActivateNewer(ctx context.Context, c native.Captured) (bool, error) {
	return m.Manager.(newerActivator).ActivateNewer(ctx, c)
}
func (m invalidGrantClaude) WriteOAuthAccount(ctx context.Context, c store.Credential) error {
	return m.Manager.(interface {
		WriteOAuthAccount(context.Context, store.Credential) error
	}).WriteOAuthAccount(ctx, c)
}

func claudeCredentials(token string, expiresAt int64) []byte {
	return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"]}}`, token+"-access", token+"-refresh", expiresAt))
}

// A turn refreshes account A in its own folder, then B becomes active. The
// background refresh must read A's newest native login, not its older saved
// copy, so it neither refreshes with a rotated token nor asks to sign in again.
func TestBackgroundRefreshReadsPinnedLoginOfInactiveAccount(t *testing.T) {
	a := testAccounts(t)
	a.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		return invalidGrantClaude{m}, ok
	}
	personal := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	work := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.Switch(context.Background(), personal.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	// A turn rotated the tokens; the saved copy still holds the old, expired ones.
	if err := os.WriteFile(filepath.Join(personal.Home, ".credentials.json"), claudeCredentials("rotated-by-turn", 4102444800000), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.transaction(func(*store.State) error {
		saved, err := readAccountLogin(personal.Home)
		if err != nil {
			return err
		}
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(saved.Credential.Payload, &payload); err != nil {
			return err
		}
		var creds map[string]json.RawMessage
		_ = json.Unmarshal(payload["credentials"], &creds)
		if creds == nil {
			creds = map[string]json.RawMessage{}
		}
		var expired map[string]json.RawMessage
		_ = json.Unmarshal(claudeCredentials("old", 1000), &expired)
		creds["claudeAiOauth"] = expired["claudeAiOauth"]
		payload["credentials"], _ = json.Marshal(creds)
		saved.Credential.Payload, _ = json.Marshal(payload)
		return writeAccountLogin(personal.Home, saved)
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.Switch(context.Background(), work.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RefreshCredentials(context.Background(), personal.ID, "/fake/native"); err != nil {
		t.Fatal("background refresh failed for an inactive account with a valid login:", err)
	}
	if err := a.transaction(func(state *store.State) error {
		if state.Find(personal.ID).NeedsLogin {
			t.Error("background refresh asked to sign in again")
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(personal.Home, ".credentials.json"))
	if err != nil || !bytes.Contains(raw, []byte("rotated-by-turn")) {
		t.Fatal("background refresh lost the rotated login", string(raw), err)
	}
}

// Preparation waits for Claude Code's refresh lock. A token that the turn
// rotates meanwhile must survive, even though the saved copy has a newer file
// time: the newer login is chosen under the lock, by expiry.
func TestTurnPinKeepsTokensRotatedWhileWaitingForTheLock(t *testing.T) {
	a := testAccounts(t)
	n := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	if err := a.Switch(context.Background(), n.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(n.Home, ".oauth_refresh.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.prepareTurnPin(context.Background(), n, "/fake/native") }()
	pin := filepath.Join(n.Home, ".credentials.json")
	if err := os.WriteFile(pin, claudeCredentials("rotated-by-turn", 4102444800000), 0600); err != nil {
		t.Fatal(err)
	}
	old := modTime(filepath.Join(n.Home, ".login.json")).Add(-time.Hour)
	if err := os.Chtimes(pin, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pin)
	if err != nil || !bytes.Contains(raw, []byte("rotated-by-turn")) {
		t.Fatal("preparation replaced a newer rotated login", string(raw), err)
	}
}

// deadlineCapture fails native capture the way a slow native reader times out.
type deadlineCapture struct{ native.Manager }

func (deadlineCapture) Capture(context.Context, string) ([]native.Captured, error) {
	return nil, context.DeadlineExceeded
}

// An activation timeout with no turn running is not a running-turn refusal.
func TestSwitchIfIdleKeepsActivationTimeouts(t *testing.T) {
	a := testAccounts(t)
	first := signedSharedGrok(t, a, "first", "first-canary")
	second := signedSharedGrok(t, a, "second", "second-canary")
	if err := a.Switch(context.Background(), first.ID, "/fake/grok"); err != nil {
		t.Fatal(err)
	}
	a.NativeManager = func(h string, host native.Host) (native.Manager, bool) {
		m, ok := NativeRuntimeManager(h, host)
		return deadlineCapture{m}, ok
	}
	err := a.SwitchIfIdle(context.Background(), second.ID, "/fake/grok")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "turns running") {
		t.Fatal("activation timeout was reported as running turns", err)
	}
}

// A room can choose another account than the globally active one. Turn
// preparation and the launch environment must read the resolved account's own
// credentials, not the global selection's, or the turn runs as another identity.
func TestClaudeRoomAccountLaunchesFromItsOwnCredentials(t *testing.T) {
	a := testAccounts(t)
	global := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	room := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.Switch(context.Background(), global.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	if err := a.SelectInScope(room, "room-a"); err != nil {
		t.Fatal(err)
	}
	shared, err := a.HarnessHome("claude-code")
	if err != nil {
		t.Fatal(err)
	}

	env, err := a.launchEnv(room.ID)
	if err != nil || env[pinnedSecretKey] != room.Home || env["CLAUDE_CONFIG_DIR"] != shared {
		t.Fatalf("room launch env = %v, %v; want the room account's credentials in the shared history home", env, err)
	}
	m, err := a.launchManager(room.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := m.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 || captured[0].Identity != room.Identity {
		t.Fatalf("readiness read %+v, %v; want the room account %q", captured, err, room.Identity)
	}

	// The global account still launches from its own folder.
	globalEnv, err := a.launchEnv(global.ID)
	if err != nil || globalEnv[pinnedSecretKey] != global.Home {
		t.Fatalf("global launch env = %v, %v", globalEnv, err)
	}
}
