package engine

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A skill saved from a host is copied over SSH, stored like any saved skill, and records the host; saving again updates it.
func TestSkillSaveFromAHostRecordsTheHost(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	runner := &outHostexec{files: skillArchive(t, "hypruse", "Drive Hyprland", nil)}
	s.SetHostexecRunner(runner)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	saved, problem := s.SkillSave(ctx, owner, contract.SkillSaveRequest{Host: "omarchy", Path: "~/.claude/skills/hypruse", Room: room})
	if problem != nil || saved.Item == nil || saved.Item.Host != "omarchy" || saved.Item.Name != "hypruse" {
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
			if item.Host != "omarchy" || item.Description != "Drive Hyprland better" {
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
