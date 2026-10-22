package configsvc

import (
	"bytes"
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
		Name: "custom", Harness: profile.HarnessCustom, RuntimeAccount: "saved-account",
		Description: "keep spacing ", Model: "first", Models: []string{"first", "second"},
		Taste: 8, Intelligence: 7, Cost: 6, ExtraArgs: []string{"--flag", " spaced "},
		Command: []string{"fixture", "{{prompt}}"}, Env: map[string]string{"SECRET": "private"},
		Output:     &workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL},
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

func TestSeedProfilesKeepsExistingAndDefault(t *testing.T) {
	isolate(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	old := mock("existing")
	old.DeclareOutput()
	old.Default, old.Env = true, map[string]string{"SECRET": "private"}
	seed(t, old)
	candidate := mock(old.Name)
	candidate.Harness = profile.HarnessCodex
	added := candidate
	added.Name, added.Default = "added", true
	unrelated := mock("unrelated")
	svc := &Service{}
	for attempt := 0; attempt < 2; attempt++ {
		rows, err := svc.SeedProfiles(ctx, profile.HarnessCodex, []profile.Profile{candidate, added, unrelated})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || !rows[0].Kept || rows[1].Kept != (attempt == 1) {
			t.Fatalf("seed results: %+v", rows)
		}
		reg, err := loadProfiles()
		if err != nil {
			t.Fatal(err)
		}
		got, ok := reg.Get(old.Name)
		if !ok || !reflect.DeepEqual(*got, old) {
			t.Fatalf("seed changed an existing profile: %#v", got)
		}
		if d, ok := reg.DefaultProfile(); !ok || d.Name != old.Name {
			t.Fatalf("seed changed default: %#v", d)
		}
		if p, ok := reg.Get(added.Name); !ok || p.Default {
			t.Fatalf("added profile: %#v", p)
		}
		if _, ok := reg.Get(unrelated.Name); ok {
			t.Fatal("seed added another harness")
		}
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
	for _, role := range []authority.Role{authority.Session, authority.Worker, authority.Steward, ""} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		_, upErr := svc.SaveProfile(ctx, ProfileEdit{})
		_, useErr := svc.SetProfileUses(ctx, "missing", nil)
		_, seedErr := svc.SeedProfiles(ctx, profile.HarnessCodex, nil)
		for _, err := range []error{upErr, useErr, seedErr, svc.UpsertProfile(ctx, mock("new")), svc.RemoveProfile(ctx, "missing"), svc.SetDefaultProfile(ctx, "missing"), svc.SetProfileDisabled(ctx, "missing", true)} {
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
