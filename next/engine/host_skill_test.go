package engine

import (
	"slices"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A skill saved from a host is copied over SSH and stored like any saved skill; the source host is not a run-on host; saving again updates it.
func TestSkillSaveFromAHostStoresTheFiles(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	runner := &outHostexec{files: skillArchive(t, "hypruse", "Drive Hyprland", nil)}
	s.SetHostexecRunner(runner)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	saved, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "omarchy", Path: "~/.claude/skills/hypruse", Room: room})
	if problem != nil || saved.Item == nil || len(saved.Item.Hosts) != 0 || saved.Item.Name != "hypruse" {
		t.Fatalf("%+v %v", saved, problem)
	}
	if len(runner.runs) != 1 || runner.runs[0].Host != "omarchy" || runner.runs[0].Argv[len(runner.runs[0].Argv)-1] != "~/.claude/skills/hypruse" {
		t.Fatalf("%+v", runner.runs)
	}
	runner.files = skillArchive(t, "hypruse", "Drive Hyprland better", nil)
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "omarchy", Path: "~/.claude/skills/hypruse", Room: room}); problem != nil {
		t.Fatal(problem)
	}
	list, problem := s.AddonList(ctx, owner, contract.AddonListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	found := 0
	for _, item := range list.Items {
		if item.Name == "hypruse" {
			found++
			if len(item.Hosts) != 0 || item.Description != "Drive Hyprland better" {
				t.Fatalf("%+v", item)
			}
		}
	}
	if found != 1 {
		t.Fatalf("found %d", found)
	}
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "nowhere", Path: "x", Room: room}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("%v", problem)
	}
}

func TestSkillSaveFromAHostCarriesRootOnly(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	s.SetHostexecRunner(&outHostexec{files: skillArchive(t, "hypruse", "Drive Hyprland", nil)})
	saved, problem := s.SkillSave(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SkillSaveRequest{Host: "omarchy", Path: "~/skills/hypruse", Room: room, RootOnly: true})
	if problem != nil || saved.Item == nil || !saved.Item.RootOnly {
		t.Fatalf("%+v %v", saved, problem)
	}
}

// Run-on hosts are set by --host whatever the source, a re-save keeps them without --host and replaces them with it,
// set changes them without the files, and every name is checked once against the registered hosts.
func TestSkillRunOnHosts(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	for _, name := range []contract.HostName{"omarchy", "desktop", "arch"} {
		if err := s.store.AddHost(ctx, box.Host{Name: name, State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &outHostexec{files: skillArchive(t, "hypruse", "Drive Hyprland", nil)}
	s.SetHostexecRunner(runner)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	hostsOf := func() []contract.HostName {
		list, problem := s.AddonList(ctx, owner, contract.AddonListRequest{Room: room})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, item := range list.Items {
			if item.Name == "hypruse" {
				return item.Hosts
			}
		}
		t.Fatal("no hypruse")
		return nil
	}
	archive := skillArchive(t, "hypruse", "Drive Hyprland", nil)
	// From a folder (the archive a box or the CLI packs) and from HOST:PATH alike; the source is not added by itself.
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Archive: archive, Room: room, Hosts: []contract.HostName{"omarchy", "desktop", "omarchy"}}); problem != nil {
		t.Fatal(problem)
	}
	if got := hostsOf(); !slices.Equal(got, []contract.HostName{"omarchy", "desktop"}) {
		t.Fatalf("local save: %v", got)
	}
	// No --host keeps them, from anywhere.
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "arch", Path: "~/hypruse", Room: room}); problem != nil {
		t.Fatal(problem)
	}
	if got := hostsOf(); !slices.Equal(got, []contract.HostName{"omarchy", "desktop"}) {
		t.Fatalf("re-save from arch: %v", got)
	}
	// --host replaces them.
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "arch", Path: "~/hypruse", Room: room, Hosts: []contract.HostName{"arch"}}); problem != nil {
		t.Fatal(problem)
	}
	if got := hostsOf(); !slices.Equal(got, []contract.HostName{"arch"}) {
		t.Fatalf("re-save with hosts: %v", got)
	}
	// set changes them and not the files; none clears them.
	runs := len(runner.runs)
	if _, problem := s.AddonSetHosts(ctx, owner, contract.AddonSetHostsRequest{Name: "hypruse", Room: room, Hosts: []contract.HostName{"omarchy", "desktop", "arch"}}); problem != nil {
		t.Fatal(problem)
	}
	if got := hostsOf(); len(got) != 3 || len(runner.runs) != runs {
		t.Fatalf("set: %v, %d runs", got, len(runner.runs)-runs)
	}
	if _, problem := s.AddonSetHosts(ctx, owner, contract.AddonSetHostsRequest{Name: "hypruse", Room: room}); problem != nil || len(hostsOf()) != 0 {
		t.Fatalf("clear: %v", problem)
	}
	// Each name is checked once, at the boundary, and nothing changes on a bad one.
	if _, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Archive: archive, Room: room, Hosts: []contract.HostName{"nowhere"}}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("save: %v", problem)
	}
	if _, problem := s.AddonSetHosts(ctx, owner, contract.AddonSetHostsRequest{Name: "hypruse", Room: room, Hosts: []contract.HostName{"arch", "nowhere"}}); problem == nil || len(hostsOf()) != 0 {
		t.Fatalf("set: %v", problem)
	}
	if _, problem := s.AddonSetHosts(ctx, owner, contract.AddonSetHostsRequest{Name: "ghost", Room: room}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("missing skill: %v", problem)
	}
}
