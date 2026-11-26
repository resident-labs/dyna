package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const agyProfilesJSON = `{
  "test": {"provider": "anthropic", "model": "m", "allowedModels": ["m"], "thinking": "low"},
  "other": {"provider": "openai", "model": "m", "allowedModels": ["m"], "thinking": "low", "harness": "pig"},
  "gem": {"provider": "google", "model": "gemini-3.8-flash", "allowedModels": ["gemini-3.8-flash"], "thinking": "low", "harness": "agy", "maxConcurrent": 2}
}`

func loadAgyProfiles(t *testing.T, text string) (*ProfileConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return LoadProfiles(path)
}

func TestProfileHarnessValidation(t *testing.T) {
	config, err := loadAgyProfiles(t, agyProfilesJSON)
	if err != nil {
		t.Fatal(err)
	}
	got := map[contract.ProfileName]contract.Harness{}
	for _, p := range config.List() {
		got[p.Name] = p.Harness
	}
	if got["test"] != contract.HarnessPig || got["other"] != contract.HarnessPig || got["gem"] != contract.HarnessAgy {
		t.Fatalf("harnesses %v", got)
	}
	for name, text := range map[string]string{
		"unknown harness":    `{"x": {"provider": "anthropic", "model": "m", "allowedModels": ["m"], "thinking": "low", "harness": "codex"}}`,
		"agy needs google":   `{"x": {"provider": "anthropic", "model": "m", "allowedModels": ["m"], "thinking": "low", "harness": "agy"}}`,
		"pig refuses google": `{"x": {"provider": "google", "model": "m", "allowedModels": ["m"], "thinking": "low"}}`,
	} {
		if _, err := loadAgyProfiles(t, text); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if textOnly := config.TextOnlyModels(); len(textOnly) != 2 {
		t.Fatalf("an agy profile's model is not Pi's: %v", textOnly)
	}
}

func agySetup(t *testing.T) (*Service, contract.RoomID, contract.SessionID, contract.Principal) {
	t.Helper()
	s, room, worker, ctx := setup(t)
	config, err := loadAgyProfiles(t, agyProfilesJSON)
	if err != nil {
		t.Fatal(err)
	}
	s.profiles = config
	if err := s.store.AddHost(ctx, box.Host{Name: "hermeslab", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	return s, room, worker, contract.Principal{Kind: contract.PrincipalOwner}
}

func TestAgySessionRunsOnHermeslabOnly(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	for _, req := range []contract.SessionStartRequest{
		{Room: room, Name: "a", Profile: "gem", Host: "host"},
		{Room: room, Name: "b", Profile: "gem", SameHost: true},
	} {
		if _, e := s.SessionStart(ctx, owner, req); e == nil || !strings.Contains(e.Message, "runs only on host hermeslab") || !strings.Contains(e.Message, "pick a pig profile") {
			t.Fatalf("%+v: %v", req, e)
		}
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "c", Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "d", Profile: "gem", Host: "hermeslab"}); e != nil {
		t.Fatal(e)
	}
	// A pig profile still goes where it is told.
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "e", Profile: "other", Host: "host"}); e != nil {
		t.Fatal(e)
	}
}

func TestSessionCannotMoveBetweenPigAndAgy(t *testing.T) {
	s, room, worker, owner := agySetup(t)
	ctx := t.Context()
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: worker, Profile: "gem"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "start a new session on gem") {
		t.Fatalf("pig to agy: %v", e)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: worker, Profile: "other"}); e != nil {
		t.Fatalf("pig to pig: %v", e)
	}
	agy, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "gemini", Profile: "gem"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: agy.Session, Profile: "test"}); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("agy to pig: %v", e)
	}
}

func TestProfileEditsKeepSessionsOnTheirHarness(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "gemini", Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	flip := contract.Profile{Name: "gem", Provider: contract.ModelProviderAnthropic, Model: "gemini-3.8-flash", AllowedModels: []string{"gemini-3.8-flash"}, Thinking: contract.ThinkingLow, Harness: contract.HarnessPig}
	if _, e := s.ProfileSet(ctx, owner, contract.ProfileSetRequest{Profile: flip}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "make a new profile") {
		t.Fatalf("flip of a used profile: %v", e)
	}
	if _, e := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "gem", Replacement: "test"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "same harness") {
		t.Fatalf("replacement on another harness: %v", e)
	}
	// An unused profile may change harness, and an omitted harness is pig.
	flip.Name = "other"
	if _, e := s.ProfileSet(ctx, owner, contract.ProfileSetRequest{Profile: flip}); e != nil {
		t.Fatalf("unused: %v", e)
	}
	for _, p := range s.profiles.List() {
		if p.Name == "other" && p.Harness != contract.HarnessPig {
			t.Fatalf("harness %q", p.Harness)
		}
	}
}

func TestAgyProfilesStayOutOfWorkflowsAndAdvisor(t *testing.T) {
	s, room, _, _ := agySetup(t)
	if e := s.setAdvisor(t.Context(), room, "gem"); e == nil || !strings.Contains(e.Message, "only on PiG profiles") {
		t.Fatalf("advisor: %v", e)
	}
	if e := s.setAdvisor(t.Context(), room, "other"); e != nil {
		t.Fatal(e)
	}
}

func TestAgyProfileSkipsProxyAccounts(t *testing.T) {
	s, room, _, _ := agySetup(t)
	a := &AccountActions{service: s}
	accounts, err := a.accountsForProfile(t.Context(), room, "gem")
	if err != nil || len(accounts.models) != 0 || len(accounts.bindings) != 0 {
		t.Fatalf("%+v %v", accounts, err)
	}
}

func TestAgyProfileShownInContext(t *testing.T) {
	s, _, worker, _ := agySetup(t)
	res, problem := s.ContextSnapshot(t.Context(), boxCaller(worker), contract.ContextSnapshotRequest{Section: contract.ContextProfiles})
	if problem != nil {
		t.Fatal(problem)
	}
	var text string
	for _, sec := range res.Sections {
		if sec.Key == contract.ContextProfiles {
			text = sec.Text
		}
	}
	if !strings.Contains(text, "gem:") || !strings.Contains(text, "agy: Google's agent with its own tools plus Dyna's, on host hermeslab only") || strings.Contains(text, "other: taste 0, diagnostics 0, intelligence 0, cost 0; agy") {
		t.Fatalf("profiles section %q", text)
	}
}

func TestBoxHelloCarriesTheHarness(t *testing.T) {
	s, room, worker, owner := agySetup(t)
	ctx := t.Context()
	pig, problem := s.BoxHello(ctx, boxCaller(worker), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if pig.Harness != contract.HarnessPig || pig.Agy != nil {
		t.Fatalf("pig hello: %q %v", pig.Harness, pig.Agy)
	}
	started, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "gemini", Profile: "gem"})
	if e != nil {
		t.Fatal(e)
	}
	connectAgent(ctx, t, s, started.Session)
	hello, problem := s.BoxHello(ctx, boxCaller(started.Session), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if hello.Harness != contract.HarnessAgy || hello.Agy == nil || hello.Agy.Binary != "/opt/agy/antigravity" || hello.Agy.GeminiDir == "" {
		t.Fatalf("agy hello: %q %+v", hello.Harness, hello.Agy)
	}
}

func TestAgyProfileLimitsOpenSessions(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	for _, name := range []string{"one", "two"} {
		if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "gem"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "three", Profile: "gem"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "allows 2 open sessions at once") {
		t.Fatalf("a third agy session: %v", e)
	}
}
