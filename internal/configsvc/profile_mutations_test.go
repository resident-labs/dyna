package configsvc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestFullProfileOperationsPreserveFields(t *testing.T) {
	isolate(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root})
	p := profile.Profile{
		Name: "pi-owned", Harness: profile.HarnessPi,
		Description: "keep spacing ", Model: "first", Models: []string{"first", "second"},
		Taste: 8, Intelligence: 7, Cost: 6, ExtraArgs: []string{"--flag", " spaced "},
		Env:        map[string]string{"SECRET": "private"},
		Output:     &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON},
		TimeoutSec: 2100, Default: true, Managed: true, SafeMode: true,
		Uses: []string{profile.UseAgents}, MaxConcurrent: 3, MaxCallsPerRun: 4,
	}
	svc := &Service{}
	if err := svc.UpsertProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	read := func() profile.Profile {
		t.Helper()
		reg, err := loadProfiles()
		if err != nil {
			t.Fatal(err)
		}
		got, ok := reg.Get(p.Name)
		if !ok {
			t.Fatal("profile missing")
		}
		return *got
	}
	if got := read(); !reflect.DeepEqual(got, p) {
		t.Fatalf("full profile changed: got %#v, want %#v", got, p)
	}
	effective, err := svc.SetProfileUses(ctx, p.Name, []string{profile.UseServices, profile.UseWorkflows})
	if err != nil {
		t.Fatal(err)
	}
	p.Uses = []string{profile.UseWorkflows, profile.UseServices}
	if !slices.Equal(effective, p.Uses) {
		t.Fatalf("effective uses: %v", effective)
	}
	if got := read(); !reflect.DeepEqual(got, p) {
		t.Fatalf("use change lost profile fields: %#v", got)
	}
	// A manual add replaces all fields, including the old marker and declaration.
	replacement := mock(p.Name)
	replacement.DeclareOutput()
	if err := svc.UpsertProfile(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, replacement) {
		t.Fatalf("replacement kept old fields: %#v", got)
	}
}

func TestProfileOperationsRefuseBeforeReadingConfig(t *testing.T) {
	isolate(t)
	if err := os.MkdirAll(filepath.Dir(profile.DefaultPath()), 0700); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("not JSON")
	if err := os.WriteFile(profile.DefaultPath(), invalid, 0600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	for _, role := range []authority.Role{authority.Session, authority.Worker, ""} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		_, upErr := svc.SaveProfile(ctx, ProfileEdit{})
		_, useErr := svc.SetProfileUses(ctx, "missing", nil)
		for _, err := range []error{upErr, useErr, svc.UpsertProfile(ctx, mock("new")), svc.RemoveProfile(ctx, "missing"), svc.SetDefaultProfile(ctx, "missing"), svc.SetProfileDisabled(ctx, "missing", true)} {
			if err == nil || !strings.Contains(err.Error(), "is owner or root only") {
				t.Fatalf("role %q reached config: %v", role, err)
			}
		}
	}
	after, err := os.ReadFile(profile.DefaultPath())
	if err != nil || !bytes.Equal(after, invalid) {
		t.Fatalf("refusal changed config: %s %v", after, err)
	}
}

// A profile of a harness Dyna has retired must be removable and replaceable,
// or the owner can never repair the registry that refusing to load it protects.
func TestProfileRepairOfARetiredHarness(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, svc *Service) error
	}{
		{"remove", func(ctx context.Context, svc *Service) error { return svc.RemoveProfile(ctx, "old-codex") }},
		{"replace", func(ctx context.Context, svc *Service) error {
			return svc.UpsertProfile(ctx, profile.Profile{Name: "old-codex", Harness: profile.HarnessPi, Model: "zai/glm-5.3", Taste: 6, Intelligence: 8, Cost: 8})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			path := profile.DefaultPath()
			contents := `{"version":3,"profiles":[
				{"name":"old-codex","harness":"codex","taste":7,"intelligence":9,"cost":8},
				{"name":"kept","harness":"pi","taste":6,"intelligence":8,"cost":8}]}`
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root})
			if err := tc.run(ctx, &Service{}); err != nil {
				t.Fatalf("repair failed: %v", err)
			}
			store, err := profile.Load(profile.DefaultPath())
			if err != nil {
				t.Fatalf("the repaired registry still does not load: %v", err)
			}
			if _, ok := store.Get("kept"); !ok {
				t.Fatal("repair dropped the runnable profile")
			}
		})
	}
}
