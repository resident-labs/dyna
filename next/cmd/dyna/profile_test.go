package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func (f *fakeClient) ProfileList(context.Context, contract.ProfileListRequest) (contract.ProfileListResponse, error) {
	return contract.ProfileListResponse{Profiles: f.profiles}, nil
}
func (f *fakeClient) ProfileSet(_ context.Context, q contract.ProfileSetRequest) (contract.ProfileSetResponse, error) {
	f.profileSet = &q
	return contract.ProfileSetResponse{}, nil
}
func (f *fakeClient) ProfileRemove(_ context.Context, q contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, error) {
	f.profileRm = &q
	return contract.ProfileRemoveResponse{}, nil
}

func storedProfiles() []contract.ProfileEntry {
	off := false
	return []contract.ProfileEntry{
		{Profile: contract.Profile{Name: "opus", Provider: contract.ModelProviderAnthropic, Model: "claude-opus", Images: new(true), Thinking: contract.ThinkingHigh, AllowedModels: []string{"claude-opus", "claude-sonnet"}, Description: "best", Taste: 10, Diagnostics: 9, Intelligence: 10, Cost: 4, CompactTokens: 7, MaxConcurrent: 4}, InUse: 3},
		{Profile: contract.Profile{Name: "text", Provider: contract.ModelProviderOpenAI, Model: "glm", Images: &off, Thinking: contract.ThinkingOff, AllowedModels: []string{"glm"}}},
	}
}

func TestProfileSetMergesIntoTheStoredProfile(t *testing.T) {
	f := &fakeClient{profiles: storedProfiles()}
	runCLI(t, f, "profile", "set", "opus", "--thinking", "low")
	want := storedProfiles()[0].Profile
	want.Thinking = contract.ThinkingLow
	if f.profileSet == nil || f.profileSet.Create || !reflect.DeepEqual(f.profileSet.Profile, want) {
		t.Fatalf("a flag-only change must keep every other field: %+v", f.profileSet)
	}
	runCLI(t, f, "profile", "set", "text", "--images", "on", "--max-concurrent", "2", "--allowed-models", "glm,glm-2")
	got := f.profileSet.Profile
	if got.Images == nil || !*got.Images || got.MaxConcurrent != 2 || !reflect.DeepEqual(got.AllowedModels, []string{"glm", "glm-2"}) || got.Thinking != contract.ThinkingOff {
		t.Fatalf("several flags: %+v", got)
	}
	runCLI(t, f, "profile", "set", "opus", "--taste", "0")
	if f.profileSet.Profile.Taste != 0 || f.profileSet.Profile.Diagnostics != 9 {
		t.Fatalf("a flag set to zero must apply: %+v", f.profileSet.Profile)
	}
}

func TestProfileSetRefusals(t *testing.T) {
	for _, args := range [][]string{
		{"profile", "set", "opus"},
		{"profile", "set", "nope", "--thinking", "low"},
		{"profile", "set", "opus", "--images", "maybe"},
		{"profile", "set"},
	} {
		f := &fakeClient{profiles: storedProfiles()}
		if _, err := execCLI(f, args...); err == nil || f.profileSet != nil {
			t.Fatalf("%v: err=%v sent=%+v", args, err, f.profileSet)
		}
	}
}

func TestProfileSetCreateStartsEmpty(t *testing.T) {
	f := &fakeClient{profiles: storedProfiles()}
	runCLI(t, f, "profile", "set", "new", "--create", "--provider", "openai", "--model", "m", "--allowed-models", "m", "--thinking", "medium")
	want := contract.Profile{Name: "new", Provider: contract.ModelProviderOpenAI, Model: "m", AllowedModels: []string{"m"}, Thinking: contract.ThinkingMedium}
	if f.profileSet == nil || !f.profileSet.Create || !reflect.DeepEqual(f.profileSet.Profile, want) {
		t.Fatalf("%+v", f.profileSet)
	}
}

func TestProfileListShowRemove(t *testing.T) {
	f := &fakeClient{profiles: storedProfiles()}
	if out, want := runCLI(t, f, "profile", "list"), "opus\tanthropic claude-opus\thigh\timages on\t3 in use\ntext\topenai glm\toff\timages off\t0 in use\n"; out != want {
		t.Fatalf("list: %q", out)
	}
	want := "name: opus\ndescription: best\nprovider: anthropic\nmodel: claude-opus\nallowed-models: claude-opus,claude-sonnet\nthinking: high\nimages: on\ntaste: 10\ndiagnostics: 9\nintelligence: 10\ncost: 4\ncompact-tokens: 7\nmax-concurrent: 4\nin-use: 3\n"
	if out := runCLI(t, f, "profile", "show", "opus"); out != want {
		t.Fatalf("show: %q", out)
	}
	if _, err := execCLI(f, "profile", "show", "nope"); err == nil {
		t.Fatal("an unknown profile was shown")
	}
	runCLI(t, f, "profile", "remove", "opus", "--replacement", "text")
	if f.profileRm == nil || f.profileRm.Name != "opus" || f.profileRm.Replacement != "text" {
		t.Fatalf("%+v", f.profileRm)
	}
}

func TestProfileListAndShowTreatMissingImagesAsOn(t *testing.T) {
	f := &fakeClient{profiles: []contract.ProfileEntry{{Profile: contract.Profile{Name: "old", Provider: contract.ModelProviderOpenAI, Model: "m", Thinking: contract.ThinkingLow}}}}
	if out := runCLI(t, f, "profile", "list"); out != "old\topenai m\tlow\timages on\t0 in use\n" {
		t.Fatalf("list: %q", out)
	}
	if out := runCLI(t, f, "profile", "show", "old"); !strings.Contains(out, "images: on\n") {
		t.Fatalf("show: %q", out)
	}
}
