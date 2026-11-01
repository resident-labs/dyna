package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

// pinProfiles gives "test" a Claude model and "other" a Codex one, and "same" a second
// Claude model.
type pinProfiles struct{ fakeProfiles }

func (pinProfiles) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	id := map[contract.ProfileName]string{"test": "claude-model", "same": "claude-other", "other": "codex-model"}[name]
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: id}, []string{id}, nil
}

type pinProxy struct {
	mu          sync.Mutex
	credentials []map[string]any
	places      []map[string]any
	down        bool
}

func (p *pinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/dyna/credentials":
		if p.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"credentials": p.credentials})
	case "/v8/management/routing/status":
		if len(p.places) == 0 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"claude": p.places}})
	default:
		http.NotFound(w, r)
	}
}

func (p *pinProxy) set(id string, field string, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, credential := range p.credentials {
		if credential["id"] == id {
			credential[field] = value
		}
	}
}

func (p *pinProxy) place(index string, rank int, spent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.places = append(p.places, map[string]any{"auth_index": index, "rank": rank, "spent": spent})
}

func pinCredential(id, provider string, models ...string) map[string]any {
	return map[string]any{"id": id, "auth_index": "index-" + id, "provider": provider, "state": "connected", "models": models}
}

func pinHarness(t *testing.T) (*AccountActions, *pinProxy, contract.SessionID, context.Context) {
	t.Helper()
	s, _, session, ctx := setup(t)
	s.profiles = pinProfiles{}
	for _, account := range []struct {
		id       contract.AccountID
		proxy    string
		provider contract.AccountProvider
		label    string
	}{
		{"a", "proxy-a", contract.ProviderAnthropic, "First"},
		{"b", "proxy-b", contract.ProviderAnthropic, "Second"},
		{"c", "proxy-c", contract.ProviderOpenAI, "Codex"},
	} {
		if err := s.store.SaveProxyAuth(ctx, store.ProxyAuth{ID: account.proxy, Provider: "invented", Record: []byte("{}")}, true); err != nil {
			t.Fatal(err)
		}
		if err := s.store.BindAccount(ctx, account.id, account.proxy, account.provider, account.label); err != nil {
			t.Fatal(err)
		}
	}
	fake := &pinProxy{credentials: []map[string]any{
		pinCredential("proxy-a", "claude", "claude-model", "claude-other"),
		pinCredential("proxy-b", "claude", "claude-model", "claude-other"),
		pinCredential("proxy-c", "codex", "codex-model"),
	}}
	// The proxy's socket path has to stay short, so it cannot live under the test's name.
	dir, err := os.MkdirTemp("/tmp", "pin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	proxy, err := model.NewProxy(dir, filepath.Join(dir, "auth"), "invented-management-key")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fake}
	go func() { _ = server.Serve(proxy.Listener) }()
	t.Cleanup(func() { _ = server.Close() })
	actions := NewAccountActions(s, proxy.Connections("invented-management-key", s.store, time.Now), proxy.Quotas("invented-management-key", s.store, time.Now))
	return actions, fake, session, ctx
}

var pinOwner = contract.Principal{Kind: contract.PrincipalOwner}

func TestSessionAccountOffersSignedInAccountsForTheModel(t *testing.T) {
	actions, fake, session, ctx := pinHarness(t)
	fake.set("proxy-b", "state", "sign_in_expired")
	response, problem := actions.SessionAccount(ctx, pinOwner, contract.SessionAccountRequest{Session: session})
	if problem != nil {
		t.Fatal(problem)
	}
	if response.Account != "" || len(response.Choices) != 1 || response.Choices[0].ID != "a" || response.Choices[0].Label != "First" {
		t.Fatalf("response = %+v", response)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "proxy-a") || strings.Contains(string(data), "index-") {
		t.Fatalf("response names a private proxy ID: %s", data)
	}
}

func TestSessionAccountHonorsARoomsAccountPolicy(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	other, problem := actions.service.RoomCreate(ctx, pinOwner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := actions.service.store.SetAccountRooms(ctx, "b", []contract.RoomID{other.Room}); err != nil {
		t.Fatal(err)
	}
	if _, problem := actions.SessionSetAccount(ctx, pinOwner, contract.SessionSetAccountRequest{Session: session, Account: "b"}); problem == nil {
		t.Fatal("chose an account this room may not use")
	}
}

func TestSessionAccountChooseAndReturnToAutomatic(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	if _, problem := actions.SessionSetAccount(ctx, pinOwner, contract.SessionSetAccountRequest{Session: session, Account: "b"}); problem != nil {
		t.Fatal(problem)
	}
	response, problem := actions.SessionAccount(ctx, pinOwner, contract.SessionAccountRequest{Session: session})
	if problem != nil || response.Account != "b" || response.Label != "Second" || response.Issue != "" {
		t.Fatalf("response = %+v, %v", response, problem)
	}
	proxyID, err := actions.service.store.SessionPinnedProxyID(ctx, session)
	if err != nil || proxyID != "proxy-b" {
		t.Fatalf("route would send %q, %v", proxyID, err)
	}
	if _, problem := actions.SessionSetAccount(ctx, pinOwner, contract.SessionSetAccountRequest{Session: session}); problem != nil {
		t.Fatal(problem)
	}
	if proxyID, err := actions.service.store.SessionPinnedProxyID(ctx, session); err != nil || proxyID != "" {
		t.Fatalf("after Automatic: %q, %v", proxyID, err)
	}
}

// A box's caller is a session principal. It must not read or change any choice, its own
// or a child's, even though it may manage those sessions in other ways.
func TestBoxCannotReadOrSetAPin(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	parent, err := actions.service.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	child, problem := actions.service.SessionStart(ctx, pinOwner, contract.SessionStartRequest{Room: parent.Room, Name: "child", Profile: "test", Parent: session})
	if problem != nil {
		t.Fatal(problem)
	}
	box := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	for _, target := range []contract.SessionID{session, child.Session} {
		if _, problem := actions.SessionSetAccount(ctx, box, contract.SessionSetAccountRequest{Session: target, Account: "b"}); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("set on %s: %v", target, problem)
		}
		if _, problem := actions.SessionAccount(ctx, box, contract.SessionAccountRequest{Session: target}); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("read on %s: %v", target, problem)
		}
		if pin, err := actions.service.store.SessionAccountPin(ctx, target); err != nil || pin != "" {
			t.Fatalf("pin = %q, %v", pin, err)
		}
	}
	if _, problem := actions.SessionSetAccount(ctx, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionSetAccountRequest{Session: session, Account: "b"}); problem == nil {
		t.Fatal("the engine principal set a pin")
	}
}

func TestSessionAccountRefusesAnAccountThatCannotServe(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	for _, account := range []contract.AccountID{"c", "missing"} {
		if _, problem := actions.SessionSetAccount(ctx, pinOwner, contract.SessionSetAccountRequest{Session: session, Account: account}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("account %s: %v", account, problem)
		}
	}
}

func TestSessionAccountSaysWhyItFellBack(t *testing.T) {
	t.Run("spent", func(t *testing.T) {
		actions, fake, session, ctx := pinHarness(t)
		fake.place("index-proxy-a", 2, true)
		pinAccount(t, actions, session, "a")
		assertIssue(t, actions, session, ctx, contract.SessionAccountSpent)
	})
	t.Run("cooling down", func(t *testing.T) {
		actions, fake, session, ctx := pinHarness(t)
		fake.place("index-proxy-b", 1, false)
		pinAccount(t, actions, session, "a")
		assertIssue(t, actions, session, ctx, contract.SessionAccountUnavailable)
	})
	t.Run("signed out", func(t *testing.T) {
		actions, fake, session, ctx := pinHarness(t)
		pinAccount(t, actions, session, "a")
		fake.set("proxy-a", "state", "sign_in_expired")
		assertIssue(t, actions, session, ctx, contract.SessionAccountSignedOut)
	})
	t.Run("removed", func(t *testing.T) {
		actions, _, session, ctx := pinHarness(t)
		pinAccount(t, actions, session, "a")
		if err := actions.service.store.RemoveAccount(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		assertIssue(t, actions, session, ctx, contract.SessionAccountRemoved)
		if proxyID, err := actions.service.store.SessionPinnedProxyID(ctx, session); err != nil || proxyID != "" {
			t.Fatalf("a removed account still routes: %q, %v", proxyID, err)
		}
	})
	t.Run("no routing order says nothing", func(t *testing.T) {
		actions, _, session, ctx := pinHarness(t)
		pinAccount(t, actions, session, "a")
		assertIssue(t, actions, session, ctx, "")
	})
}

func pinAccount(t *testing.T, actions *AccountActions, session contract.SessionID, account contract.AccountID) {
	t.Helper()
	if _, problem := actions.SessionSetAccount(context.Background(), pinOwner, contract.SessionSetAccountRequest{Session: session, Account: account}); problem != nil {
		t.Fatal(problem)
	}
}

func assertIssue(t *testing.T, actions *AccountActions, session contract.SessionID, ctx context.Context, want contract.SessionAccountIssue) {
	t.Helper()
	response, problem := actions.SessionAccount(ctx, pinOwner, contract.SessionAccountRequest{Session: session})
	if problem != nil || response.Issue != want {
		t.Fatalf("response = %+v, %v, want issue %q", response, problem, want)
	}
}

func TestProfileChangeDropsAPinTheNewModelCannotUse(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	pinAccount(t, actions, session, "a")
	if _, problem := actions.service.SessionSetProfile(ctx, pinOwner, contract.SessionSetProfileRequest{Session: session, Profile: "same"}); problem != nil {
		t.Fatal(problem)
	}
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "a" {
		t.Fatalf("same provider: pin = %q, %v", pin, err)
	}
	if _, problem := actions.service.SessionSetProfile(ctx, pinOwner, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatal(problem)
	}
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "" {
		t.Fatalf("other provider: pin = %q, %v", pin, err)
	}
	if _, problem := actions.service.SessionSetProfile(ctx, pinOwner, contract.SessionSetProfileRequest{Session: session, Profile: "test"}); problem != nil {
		t.Fatal(problem)
	}
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "" {
		t.Fatalf("a dropped pin came back: %q, %v", pin, err)
	}
}

func TestProfileChangeSurvivesAnUnreadableProxy(t *testing.T) {
	actions, fake, session, ctx := pinHarness(t)
	pinAccount(t, actions, session, "a")
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	if _, problem := actions.service.SessionSetProfile(ctx, pinOwner, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatalf("the profile change failed on the pin: %v", problem)
	}
}

// pinRealProfiles uses ProfileConfig, because only its lock can deadlock a hook that runs
// inside Replace.
func pinRealProfiles(t *testing.T, s *Service) *ProfileConfig {
	t.Helper()
	entry := func(model string) map[string]any {
		return map[string]any{"provider": "anthropic", "model": model, "allowedModels": []string{model}, "thinking": "off"}
	}
	data, err := json.Marshal(map[string]any{"test": entry("claude-model"), "same": entry("claude-other"), "other": entry("codex-model")})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	s.profiles = profiles
	return profiles
}

func removeProfile(t *testing.T, s *Service, ctx context.Context, name, replacement contract.ProfileName) *contract.Error {
	t.Helper()
	done := make(chan *contract.Error, 1)
	go func() {
		_, problem := s.ProfileRemove(ctx, pinOwner, contract.ProfileRemoveRequest{Name: name, Replacement: replacement})
		done <- problem
	}()
	select {
	case problem := <-done:
		return problem
	case <-time.After(10 * time.Second):
		t.Fatal("profile.remove did not finish: the hook is waiting on the profile lock")
		return nil
	}
}

func TestProfileRemoveDropsPinsTheReplacementCannotUse(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	pinRealProfiles(t, actions.service)
	sessions, err := actions.service.store.SessionsOnProfile(ctx, "test")
	if err != nil || len(sessions) < 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	for _, target := range sessions {
		pinAccount(t, actions, target.ID, "a")
	}
	if problem := removeProfile(t, actions.service, ctx, "test", "other"); problem != nil {
		t.Fatal(problem)
	}
	for _, target := range sessions {
		if pin, err := actions.service.store.SessionAccountPin(ctx, target.ID); err != nil || pin != "" {
			t.Fatalf("session %s: pin = %q, %v", target.ID, pin, err)
		}
	}
	moved, err := actions.service.store.Session(ctx, session)
	if err != nil || moved.Profile != "other" {
		t.Fatalf("session = %+v, %v", moved, err)
	}
}

func TestProfileRemoveKeepsAPinTheReplacementCanUse(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	pinRealProfiles(t, actions.service)
	pinAccount(t, actions, session, "a")
	if problem := removeProfile(t, actions.service, ctx, "test", "same"); problem != nil {
		t.Fatal(problem)
	}
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "a" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
}

func TestRefusedProfileRemoveKeepsThePin(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	pinRealProfiles(t, actions.service)
	pinAccount(t, actions, session, "a")
	if problem := removeProfile(t, actions.service, ctx, "test", "missing"); problem == nil {
		t.Fatal("an unknown replacement was accepted")
	}
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "a" {
		t.Fatalf("pin = %q, %v", pin, err)
	}
}

// Editing a profile moves its sessions to the new model at their next turn, with no move.
func TestProfileEditDropsAPinTheNewModelCannotUse(t *testing.T) {
	actions, _, session, ctx := pinHarness(t)
	pinRealProfiles(t, actions.service)
	pinAccount(t, actions, session, "a")
	edit := func(model string) {
		t.Helper()
		profile := contract.Profile{Name: "test", Provider: contract.ModelProviderAnthropic, Model: model, AllowedModels: []string{model}, Thinking: contract.ThinkingOff}
		if _, problem := actions.service.ProfileSet(ctx, pinOwner, contract.ProfileSetRequest{Profile: profile}); problem != nil {
			t.Fatal(problem)
		}
	}
	edit("claude-other")
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "a" {
		t.Fatalf("same provider: pin = %q, %v", pin, err)
	}
	edit("codex-model")
	if pin, err := actions.service.store.SessionAccountPin(ctx, session); err != nil || pin != "" {
		t.Fatalf("other provider: pin = %q, %v", pin, err)
	}
}
