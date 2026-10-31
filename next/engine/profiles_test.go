package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestProfilesLoadLegacySurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low","agentSurface":"caller"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	choice, _, err := profiles.Model("p")
	if err != nil || choice.ID != "a" {
		t.Fatalf("model: %+v, %v", choice, err)
	}
	if problem := profiles.update("p", &profile{Provider: contract.ModelProviderAnthropic, Model: "a", AllowedModels: []string{"a"}, Thinking: contract.ThinkingLow}, false); problem != nil {
		t.Fatal(problem)
	}
	saved, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(saved), "agentSurface") {
		t.Fatalf("saved profile: %s, %v", saved, err)
	}
}

func TestProfilesRejectModelOutsideAllowList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"p":{"provider":"anthropic","model":"a","allowedModels":["b"],"thinking":"low"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("expected invalid profile")
	}
}

func TestProfilesReturnModelChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"xhigh"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	choice, allowed, err := p.Model("p")
	if err != nil || choice.ID != "a" || choice.Thinking != contract.ThinkingXHigh || len(allowed) != 1 || allowed[0] != "a" {
		t.Fatalf("%+v %v %v", choice, allowed, err)
	}
}

func TestProfilesRejectNegativeLimits(t *testing.T) {
	for _, field := range []string{"maxConcurrent", "maxCallsPerRun"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			data := `{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low","` + field + `":-1}}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(path); err == nil {
				t.Fatal("negative limit accepted")
			}
		})
	}
}

func TestProfileRatings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	config := `{"writer":{"provider":"anthropic","model":"m","allowedModels":["m"],"thinking":"low","description":"Clear writing","taste":10,"diagnostics":8,"intelligence":7,"cost":6}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	got := profiles.List()[0]
	if got.Description != "Clear writing" || got.Taste != 10 || got.Diagnostics != 8 || got.Intelligence != 7 || got.Cost != 6 {
		t.Fatalf("ratings lost: %+v", got)
	}
	for _, invalid := range []string{"-1", "11"} {
		bad := strings.Replace(config, `"taste":10`, `"taste":`+invalid, 1)
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProfiles(path); err == nil {
			t.Fatalf("accepted rating %s", invalid)
		}
	}
}

// A removal cannot pass its usage check while an assignment is committing.

func TestProfileRemovalWaitsForAssignment(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"p":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low"},"q":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	s.profiles = p
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := store.Session{ID: "profile-race", Room: room, Name: "profile-race", Profile: "p", Box: "profile-race-box"}
	event, err := s.event(room, owner, contract.SessionStarted{Session: session.ID, Parent: root, Name: session.Name, Profile: "p"})
	if err != nil {
		t.Fatal(err)
	}
	boxEvent, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	started := make(chan *contract.Error, 1)
	go func() {
		started <- p.WithProfile("p", func() *contract.Error {
			close(entered)
			<-release
			if _, err := s.store.StartSession(ctx, session, root, contract.BoxSizeAgent, "", event, boxEvent, "", false, "", 0); err != nil {
				return failure(err)
			}
			return nil
		})
	}()
	<-entered
	removed := make(chan *contract.Error, 1)
	go func() {
		_, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "p"})
		removed <- problem
	}()
	close(release)
	if problem := <-started; problem != nil {
		t.Fatal(problem)
	}
	if problem := <-removed; problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("removal: %v", problem)
	}
}
