package serviceregistry

import (
	"testing"
	"time"
)

func TestBuildListsEveryServiceWithGroupAndSwitch(t *testing.T) {
	hook := HookFacts{Found: true}
	services := Build(Facts{
		Reviewer: &ReviewerFacts{Enabled: true},
		Merge:    &MergeFacts{},
		Library:  &LibraryFacts{Enabled: true},
		Voice:    &VoiceFacts{Available: true},
		Hooks:    map[ID]HookFacts{IDUpdates: hook, IDLoadDoctor: hook, IDMaintenance: hook, IDAccountUsage: hook, IDHarnessVersions: hook, IDParentCheckIns: hook},
	})
	want := []struct {
		id        ID
		group     Group
		hasSwitch bool
		state     State
	}{
		{IDReviews, GroupPullRequests, true, StateIdle},
		{IDMergeQueue, GroupPullRequests, true, StateIdle},
		{IDLocalCI, GroupPullRequests, false, StateOn},
		{IDPRWatcher, GroupPullRequests, false, StateOn},
		{IDLaneSync, GroupPullRequests, false, StateNotSetUp},
		{IDIssueWatch, GroupPullRequests, false, StateNotSetUp},
		{IDLibrary, GroupAgents, true, StateIdle},
		{IDParentCheckIns, GroupAgents, true, StateWaiting},
		{IDVoice, GroupAgents, false, StateIdle},
		{IDUpdates, GroupThisComputer, true, StateWaiting},
		{IDLoadDoctor, GroupThisComputer, true, StateWaiting},
		{IDMaintenance, GroupThisComputer, true, StateWaiting},
		{IDAccountUsage, GroupThisComputer, true, StateWaiting},
		{IDHarnessVersions, GroupThisComputer, true, StateWaiting},
	}
	if len(services) != len(want) {
		t.Fatalf("got %d services, want %d", len(services), len(want))
	}
	for i, w := range want {
		got := services[i]
		if got.ID != w.id || got.Group != w.group || (got.Switch != nil) != w.hasSwitch || got.State != w.state {
			t.Errorf("service %d = %s/%s switch=%v state=%s, want %s/%s switch=%v state=%s", i, got.ID, got.Group, got.Switch != nil, got.State, w.id, w.group, w.hasSwitch, w.state)
		}
	}
}

// D-556: the status follows the newest run of the hook, whatever started it.
func TestHookStatusUsesNewestRun(t *testing.T) {
	at := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	hook := HookFacts{Found: true, Runs: []HookRun{
		{Result: "pass", StartedAt: at.Add(time.Hour)},
		{Result: "error", StartedAt: at},
	}}
	got := Build(Facts{Hooks: map[ID]HookFacts{IDUpdates: hook}})[9]
	if got.ID != IDUpdates || got.State != StateOn || got.Detail != DetailNone {
		t.Fatalf("updates = %s %s %q, want on with no detail", got.ID, got.State, got.Detail)
	}
	if got.LastRun == nil || !got.LastRun.Equal(at.Add(time.Hour)) {
		t.Fatalf("last run = %v, want the newer run", got.LastRun)
	}
	hook.Runs[0].Result = "error"
	hook.Runs[1].Result = "pass"
	if got := Build(Facts{Hooks: map[ID]HookFacts{IDUpdates: hook}})[9]; got.State != StateError {
		t.Fatalf("newest failed run gave %s, want error", got.State)
	}
}
