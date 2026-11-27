package engine

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/engine/live"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const agyProfilesJSON = `{
  "test": {"provider": "anthropic", "model": "m", "allowedModels": ["m"], "thinking": "low"},
  "other": {"provider": "openai", "model": "m", "allowedModels": ["m"], "thinking": "low", "harness": "pig"},
  "gem": {"provider": "google", "model": "gemini-3.8-flash", "allowedModels": ["gemini-3.8-flash"], "thinking": "low", "harness": "agy", "maxConcurrent": 2},
  "gem2": {"provider": "google", "model": "gemini-3.8-pro", "allowedModels": ["gemini-3.8-pro"], "thinking": "low", "harness": "agy", "maxConcurrent": 3}
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
	if err := s.store.SetHostAgy(ctx, "hermeslab", true); err != nil {
		t.Fatal(err)
	}
	return s, room, worker, contract.Principal{Kind: contract.PrincipalOwner}
}

func TestAgySessionRunsOnAHostThatHasAgy(t *testing.T) {
	s, room, root, owner := agySetup(t)
	ctx := t.Context()
	start := func(name string, req contract.SessionStartRequest) *contract.Error {
		req.Room, req.Name, req.Profile, req.Parent = room, name, "gem", root
		_, e := s.SessionStart(ctx, owner, req)
		return e
	}
	// "host" has no agy: a requested host without it is refused with the next step, and one that has it is taken.
	if e := start("a", contract.SessionStartRequest{Host: "host"}); e == nil || !strings.Contains(e.Message, "host host does not have agy installed and signed in") || !strings.Contains(e.Message, "host list") {
		t.Fatalf("host without agy: %v", e)
	}
	if e := start("b", contract.SessionStartRequest{SameHost: true}); e == nil || !strings.Contains(e.Message, "does not have agy") {
		t.Fatalf("sameHost beside a parent on a host without agy: %v", e)
	}
	if e := start("c", contract.SessionStartRequest{}); e != nil {
		t.Fatal(e)
	}
	if e := start("d", contract.SessionStartRequest{Host: "hermeslab"}); e != nil {
		t.Fatal(e)
	}
	// A pig profile still goes where it is told.
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "e", Profile: "other", Host: "host"}); e != nil {
		t.Fatal(e)
	}
	// Once the other host has agy, it is taken, and sameHost works beside a parent there.
	if err := s.store.SetHostAgy(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	if e := start("f", contract.SessionStartRequest{Host: "host"}); e != nil {
		t.Fatalf("host with agy: %v", e)
	}
	if e := start("g", contract.SessionStartRequest{SameHost: true}); e != nil {
		t.Fatalf("sameHost on a host with agy: %v", e)
	}
}

// With no host naming agy, a new agy session is refused, and the placement's choice is among the hosts that have it.
func TestAgySessionWithNoAgyHostIsRefused(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	if err := s.store.SetHostAgy(ctx, "hermeslab", false); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "x", Profile: "gem"}); e == nil || !strings.Contains(e.Message, "no host") || !strings.Contains(e.Message, "signs in to Antigravity") {
		t.Fatalf("%v", e)
	}
	// An exec-only host or one with agents off never counts, even if it reports agy.
	if err := s.store.AddHost(ctx, box.Host{Name: "exec", State: box.HostReady, ExecOnly: true, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetHostAgy(ctx, "exec", true); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "y", Profile: "gem"}); e == nil || !strings.Contains(e.Message, "no host") {
		t.Fatalf("an exec-only host with agy: %v", e)
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
	if !strings.Contains(text, "gem:") || !strings.Contains(text, "harness agy: Google's agent with its own tools plus Dyna's, on the hosts that host list marks agy") || strings.Contains(text, "other: taste 0, diagnostics 0, intelligence 0, cost 0; agy") {
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

func TestProfileSetRefusesAnAntigravityModel(t *testing.T) {
	s, _, _, owner := agySetup(t)
	req := contract.ProfileSetRequest{Create: true, Profile: contract.Profile{Name: "neon", Provider: contract.ModelProviderAnthropic, Model: "antigravity/gemini", AllowedModels: []string{"antigravity/gemini"}, Thinking: contract.ThinkingLow}}
	if _, e := s.ProfileSet(t.Context(), owner, req); e == nil || !strings.Contains(e.Message, "--harness agy") {
		t.Fatalf("%v", e)
	}
}

func TestDiscordRouteRefusesAnAgySession(t *testing.T) {
	f := discordFixture(t)
	config, err := loadAgyProfiles(t, agyProfilesJSON)
	if err != nil {
		t.Fatal(err)
	}
	f.s.profiles = config
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := f.s.store.AddHost(f.ctx, box.Host{Name: "hermeslab", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: f.s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.SetHostAgy(f.ctx, "hermeslab", true); err != nil {
		t.Fatal(err)
	}
	if _, e := f.s.SessionStart(f.ctx, owner, contract.SessionStartRequest{Room: f.room, Name: "gemini", Profile: "gem"}); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.RoomDiscord(f.ctx, owner, contract.RoomDiscordRequest{Room: f.room, Server: "100", Session: "gemini"})
	if e == nil || !strings.Contains(e.Message, "name a session on a pig profile") {
		t.Fatalf("%v", e)
	}
}

// With no turn running, a stop reaches an agy harness as a turn-less stop, and the response says something stopped: work the last
// turn left behind runs on. A running turn gets the named stop from the stream instead.
func TestStopReachesAnAgyHarnessWithNoTurn(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	started, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "gemini", Profile: "gem"})
	if e != nil {
		t.Fatal(e)
	}
	// The session has a live channel and no turn.
	handle, _ := s.live.CreateSession(started.Session)
	handle.Lock()
	channel := live.NewChannel(func() {})
	handle.SetChannel(channel)
	handle.Unlock()
	stopped, e := s.SessionStop(ctx, owner, contract.SessionStopRequest{Session: started.Session})
	if e != nil || !stopped.Stopping {
		t.Fatalf("stop: %+v %v", stopped, e)
	}
	handle.Lock()
	sent := channel.TakeStop()
	handle.Unlock()
	if !sent {
		t.Fatal("the stop did not reach the agy harness")
	}
}

// An agy profile's max-concurrent is what it is for any profile: no account cap, so more sessions than it names start, and 0 is valid.
func TestAgyProfileHasNoSessionCap(t *testing.T) {
	if _, err := loadAgyProfiles(t, `{"g": {"provider": "google", "model": "gemini-3.8-flash", "allowedModels": ["gemini-3.8-flash"], "thinking": "low", "harness": "agy"}}`); err != nil {
		t.Fatal(err)
	}
	s, room, _, owner := agySetup(t)
	for _, name := range []string{"a", "b", "c", "d"} {
		if _, e := s.SessionStart(t.Context(), owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "gem"}); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
	}
}

func TestAgySeedIsTheOwnersAndNeverForARunningOrIneligibleHost(t *testing.T) {
	s, _, root, owner := agySetup(t)
	ctx := t.Context()
	if e := s.AgySeedProblem(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, "host"); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("a session seeds: %v", e)
	}
	if e := s.AgySeedProblem(ctx, owner, AgySource); e == nil || !strings.Contains(e.Message, "where the owner signed in") {
		t.Fatalf("the source: %v", e)
	}
	if e := s.AgySeedProblem(ctx, owner, "nowhere"); e == nil || !strings.Contains(e.Message, "unknown host") {
		t.Fatalf("unknown: %v", e)
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "exec", State: box.HostReady, ExecOnly: true, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if e := s.AgySeedProblem(ctx, owner, "exec"); e == nil || !strings.Contains(e.Message, "exec-only or has agents off") {
		t.Fatalf("exec-only: %v", e)
	}
	if e := s.AgySeedProblem(ctx, owner, "host"); e != nil {
		t.Fatalf("a host with only a pig session: %v", e)
	}
}

// A session switched from agy to Pi has its old agy box, with the account mounted, until that box restarts: the seed looks at what runs,
// not at the profile.

func TestAgySeedRefusesAnOldAgyBoxDuringAHarnessSwitch(t *testing.T) {
	s, room, _, owner := agySetup(t)
	ctx := t.Context()
	if err := s.store.SetHostAgy(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	sid := startOn(t, s, room, "mover", "gem", "host")
	readySession(t, s, sid)
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{CanHandoff: true, LaunchGen: launchGen(t, s, sid)})
	if problem != nil || hello.Harness != contract.HarnessAgy {
		t.Fatalf("hello: %+v %v", hello, problem)
	}
	openTurn(t, s, ctx, room, sid, "t1")
	if _, p := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: sid, Profile: "other"}); p != nil {
		t.Fatal(p)
	}
	allowed, err := s.AgyExitAllowed(ctx, hello.Box)
	if err != nil || !allowed {
		t.Fatalf("expected old agy runtime while turn runs: %v %v", allowed, err)
	}
	if p := s.BeginAgySeed(ctx, "host", "tok", ""); p == nil {
		t.Fatal("seed admitted on host with a still-running agy turn")
	}
}

// seeding marks the host as a seed holds it, as the seed's own transaction does, without the check of boxes.
func markSeeding(t *testing.T, s *Service, host contract.HostName, token string) {
	t.Helper()
	if err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE hosts SET seeding=? WHERE name=?", token, host)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedMark(t *testing.T, s *Service, host contract.HostName) string {
	t.Helper()
	mark, err := s.AgySeedMark(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	return mark
}

// The persisted seed mark, one cell of the states and events table at a time: what each event does while the host is idle and while a
// seed holds it.
func TestAgySeedMarkStatesAndEvents(t *testing.T) {
	agyHost := func(t *testing.T) (*Service, contract.RoomID, contract.SessionID, contract.Principal) {
		s, room, root, owner := agySetup(t)
		if err := s.store.SetHostAgy(t.Context(), "host", true); err != nil {
			t.Fatal(err)
		}
		return s, room, root, owner
	}
	start := func(s *Service, owner contract.Principal, room contract.RoomID, root contract.SessionID, name string) *contract.Error {
		_, e := s.SessionStart(t.Context(), owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "gem", Parent: root, Host: "host"})
		return e
	}
	cells := map[string]func(t *testing.T){
		"seed start on an idle host with no agy box marks it": func(t *testing.T) {
			s, _, _, _ := agyHost(t)
			if e := s.BeginAgySeed(t.Context(), "host", "one", ""); e != nil || seedMark(t, s, "host") != "one" {
				t.Fatalf("%v %q", e, seedMark(t, s, "host"))
			}
		},
		"seed start refuses a host with an agy box, pending or running, and marks nothing": func(t *testing.T) {
			s, room, root, owner := agyHost(t)
			if e := start(s, owner, room, root, "gemini"); e != nil {
				t.Fatal(e)
			}
			if e := s.BeginAgySeed(t.Context(), "host", "one", ""); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "gemini") || seedMark(t, s, "host") != "" {
				t.Fatalf("%v %q", e, seedMark(t, s, "host"))
			}
		},
		"seed start on a marked host is refused unless it takes over the mark it read": func(t *testing.T) {
			s, _, _, _ := agyHost(t)
			markSeeding(t, s, "host", "stale")
			if e := s.BeginAgySeed(t.Context(), "host", "two", ""); e == nil || e.Code != contract.ErrConflict || seedMark(t, s, "host") != "stale" {
				t.Fatalf("a seed took another's mark: %v %q", e, seedMark(t, s, "host"))
			}
			if e := s.BeginAgySeed(t.Context(), "host", "two", "stale"); e != nil || seedMark(t, s, "host") != "two" {
				t.Fatalf("%v %q", e, seedMark(t, s, "host"))
			}
		},
		"seed end clears its own mark and no other's": func(t *testing.T) {
			s, _, _, _ := agyHost(t)
			markSeeding(t, s, "host", "mine")
			s.EndAgySeedAfter("host", "theirs", true, nil)
			if seedMark(t, s, "host") != "mine" {
				t.Fatal("another seed's end cleared the mark")
			}
			s.EndAgySeedAfter("host", "mine", true, nil)
			if seedMark(t, s, "host") != "" {
				t.Fatal("a known end left the mark")
			}
		},
		"seed end of an unknown end clears only after the barrier, and stays when it fails": func(t *testing.T) {
			s, _, _, _ := agyHost(t)
			markSeeding(t, s, "host", "tok")
			s.EndAgySeedAfter("host", "tok", false, func(context.Context, contract.HostName) error { return errors.New("unreachable") })
			if seedMark(t, s, "host") != "tok" {
				t.Fatal("the mark was cleared with the host unreachable")
			}
			s.EndAgySeedAfter("host", "tok", false, func(context.Context, contract.HostName) error { return nil })
			if seedMark(t, s, "host") != "" {
				t.Fatal("the mark stayed after the barrier passed")
			}
		},
		"engine start runs the barrier per marked host and never clears blindly": func(t *testing.T) {
			s, _, _, _ := agyHost(t)
			markSeeding(t, s, "host", "stale")
			s.ReleaseAgySeeds(t.Context(), func(context.Context, contract.HostName) error { return errors.New("unreachable") })
			if seedMark(t, s, "host") != "stale" {
				t.Fatal("an unreachable host's mark was cleared at start")
			}
			var asked []contract.HostName
			s.ReleaseAgySeeds(t.Context(), func(_ context.Context, name contract.HostName) error { asked = append(asked, name); return nil })
			if seedMark(t, s, "host") != "" || !slices.Equal(asked, []contract.HostName{"host"}) {
				t.Fatalf("%q %v", seedMark(t, s, "host"), asked)
			}
		},
		"agy session start pinned to a seeded host is refused, and an idle host takes it": func(t *testing.T) {
			s, room, root, owner := agyHost(t)
			markSeeding(t, s, "host", "tok")
			if e := start(s, owner, room, root, "during"); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "being seeded") {
				t.Fatalf("%v", e)
			}
			markSeeding(t, s, "host", "")
			if e := start(s, owner, room, root, "after"); e != nil {
				t.Fatal(e)
			}
		},
		"an unpinned agy session does not choose a seeded host": func(t *testing.T) {
			s, room, root, owner := agyHost(t)
			markSeeding(t, s, "host", "tok")
			markSeeding(t, s, "hermeslab", "tok")
			if _, e := s.SessionStart(t.Context(), owner, contract.SessionStartRequest{Room: room, Name: "any", Profile: "gem", Parent: root}); e == nil {
				t.Fatal("placed with every agy host seeded")
			}
		},
		"a switch to agy on a seeded host is refused; a switch away is allowed": func(t *testing.T) {
			s, room, _, owner := agyHost(t)
			pig := startOn(t, s, room, "pig", "other", "host")
			readySession(t, s, pig)
			markSeeding(t, s, "host", "tok")
			if _, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: pig, Profile: "gem"}); e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "being seeded") {
				t.Fatalf("%v", e)
			}
			markSeeding(t, s, "host", "")
			if _, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: pig, Profile: "gem"}); e != nil {
				t.Fatal(e)
			}
			markSeeding(t, s, "host", "tok")
			if _, e := s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: pig, Profile: "other"}); e != nil {
				t.Fatalf("a switch away during a seed: %v", e)
			}
		},
		"a box that wants agy is refused its start on a seeded host, in the start transaction": func(t *testing.T) {
			s, room, root, owner := agyHost(t)
			if e := start(s, owner, room, root, "gemini"); e != nil {
				t.Fatal(e)
			}
			sessions, _ := s.store.HostSessions(t.Context())
			index := slices.IndexFunc(sessions["host"], func(h contract.HostSession) bool { return h.Name == "gemini" })
			b, err := s.store.Box(t.Context(), s.mustSession(t, sessions["host"][index].ID).Box)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.Write(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), "UPDATE boxes SET state='pending' WHERE id=?", b.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			b.State, b.Host = contract.BoxPending, "host" // as admission assigns it
			markSeeding(t, s, "host", "tok")
			event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.TransitionBox(t.Context(), b, contract.BoxCreating, "", event); !errors.Is(err, store.ErrHostSeeding) {
				t.Fatalf("a start on a seeded host: %v", err)
			}
		},
	}
	for name, cell := range cells {
		t.Run(name, cell)
	}
}

func (s *Service) mustSession(t *testing.T, id contract.SessionID) store.Session {
	t.Helper()
	session, err := s.store.Session(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// A profile edit, a seed start and a profile switch at once never wait on each other: the seed and the switch each decide in one
// transaction, and neither holds a lock the profile registry's writer queues behind.
func TestAgySeedSwitchAndProfileEditDoNotDeadlock(t *testing.T) {
	s, room, _, owner := agySetup(t)
	pig := startOn(t, s, room, "pig", "other", "hermeslab")
	readySession(t, s, pig)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = s.ProfileSet(t.Context(), owner, contract.ProfileSetRequest{Profile: contract.Profile{Name: "other", Provider: contract.ModelProviderOpenAI, Model: "m", AllowedModels: []string{"m"}, Thinking: contract.ThinkingLow, Harness: contract.HarnessPig}})
		}()
		go func() { defer wg.Done(); _ = s.BeginAgySeed(t.Context(), "host", "tok", "") }()
		go func() {
			defer wg.Done()
			_, _ = s.SessionSetProfile(t.Context(), owner, contract.SessionSetProfileRequest{Session: pig, Profile: "gem"})
		}()
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a profile edit, a seed and a switch waited on each other")
	}
}
