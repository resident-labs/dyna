// Package serviceregistry is the one list of what Dyna runs by itself. The
// engine builds it from typed facts, `dyna services` prints it and the app's
// Services page reads it, so the two cannot disagree about a service.
package serviceregistry

import (
	"time"

	"dyna-agent/internal/hooks"
)

// HookRun is one run of the hook behind a service.
type HookRun struct {
	// Result is the hook run result: pass, error, fail or pending.
	Result     hooks.RunResult
	StartedAt  time.Time
	FinishedAt time.Time
}

// HookFacts describes the hook that schedules a service. Runs may come in any
// order and from any event: the newest one by start time decides the status.
type HookFacts struct {
	Found    bool
	Disabled bool
	// Schedule is set when the hook is a built-in whose timing the owner edits.
	Schedule *Schedule
	Runs     []HookRun
	NextRun  *time.Time
}

// ReviewerFacts are the reviewer pool's facts for one room.
type ReviewerFacts struct {
	Enabled          bool
	ProfileWaiting   bool
	Failing          bool
	Reviewing        bool
	Queued           int
	LastVerdictAt    time.Time
	Profile, Session string
}

// MergeFacts are the merge queue's facts for one room.
type MergeFacts struct {
	Paused     bool
	Running    int
	Ready      int
	LastMerged time.Time
}

// LibraryFacts are the librarian's facts for one repository.
type LibraryFacts struct {
	Enabled        bool
	ProfileWaiting bool
	Running        bool
	LastRun        time.Time
	NextRun        *time.Time
	Profile        string
	Session        string
}

// UpdateFacts describes the source updater and its latest installer run.
type UpdateFacts struct {
	Source        string
	Enabled       bool
	SourceInvalid bool
	MultipleHooks bool
	EveryMinutes  int
	Runs          []HookRun
}

// VoiceFacts are the voice service's facts.
type VoiceFacts struct {
	Available bool
	Live      bool
}

// Facts is everything the engine reads to build the registry. A nil pointer
// means the engine could not read that part, and the row says so.
type Facts struct {
	Reviewer *ReviewerFacts
	Merge    *MergeFacts
	// LaneRemotes counts configured lane sync remotes; LaneFailing is true
	// when any remote's newest check failed.
	LaneRemotes   int
	LaneFailing   bool
	LaneCheckedAt time.Time
	// IssueWatches counts the room's watched issues.
	IssueWatches int
	Library      *LibraryFacts
	Voice        *VoiceFacts
	Updates      *UpdateFacts
	// Hooks holds the hook behind each hook-backed service.
	Hooks map[ID]HookFacts
}

// UpdatesHook is the repository hook that installs a new Dyna build; the
// Updates service reads and switches it.
const UpdatesHook = "self-update"

type definition struct {
	id       ID
	group    Group
	label    string
	describe string
	keys     []string
}

var definitions = []definition{
	{IDReviews, GroupPullRequests, "Reviews", "Agents review each ready change before it merges.", []string{"pr.outsideComments", "pr.trustedBots"}},
	{IDMergeQueue, GroupPullRequests, "Merge queue", "Tests ready changes together and merges the ones that pass.", nil},
	{IDLocalCI, GroupPullRequests, "Local CI", "Decides what a test run may cost and runs checks on this computer.", []string{"admission.mode", "admission.maxAgents"}},
	{IDPRWatcher, GroupPullRequests, "PR watcher", "Follows pull requests on GitHub and warns when changes overlap.", nil},
	{IDLaneSync, GroupPullRequests, "Lane sync", "Keeps a local branch lane in step with its remote.", []string{"lane.local.repos"}},
	{IDIssueWatch, GroupPullRequests, "Issue watch", "Tells a room when a watched GitHub issue changes.", nil},
	{IDLibrary, GroupAgents, "Library", "An agent keeps the feature map and docs current.", []string{"library.enabled", "library.profile", "library.smellProfile", "library.smellsPerDay", "library.readmeMaxLines"}},
	{IDParentCheckIns, GroupAgents, "Parent check-ins", "Each parent hears how its children are doing.", []string{"sessions.checkInInterval"}},
	{IDVoice, GroupAgents, "Voice", "Talk to Dyna in a call; it hands work to the room's root.", []string{"voice.agent", "voice.ducking", "voice.model", "voice.provider", "voice.url"}},
	{IDUpdates, GroupThisComputer, "Updates", "Installs a new Dyna build when main changes.", []string{"updater.source", "updater.autoCheck"}},
	{IDLoadDoctor, GroupThisComputer, "Load doctor", "Watches this computer's load and tells the rooms that cause it.", []string{"doctor.condition"}},
	{IDMaintenance, GroupThisComputer, "Maintenance", "Frees disk space and removes old worktrees and runs.", []string{"runs.retention", "worktrees.retention", "worktrees.minFreeBytes", "worktrees.root"}},
	{IDAccountUsage, GroupThisComputer, "Account usage", "Keeps the usage readings of your saved accounts fresh.", nil},
	{IDHarnessVersions, GroupThisComputer, "Harness versions", "Checks for the recommended version of each agent tool.", nil},
}

// IDs lists every service id in display order.
func IDs() []ID {
	ids := make([]ID, len(definitions))
	for i, d := range definitions {
		ids[i] = d.id
	}
	return ids
}

// Build returns the registry in display order.
func Build(f Facts) []Service {
	out := make([]Service, 0, len(definitions))
	for _, d := range definitions {
		s := Service{ID: d.id, Group: d.group, Label: d.label, Description: d.describe, SettingsKeys: d.keys}
		if s.SettingsKeys == nil {
			s.SettingsKeys = []string{}
		}
		switch d.id {
		case IDReviews:
			reviews(&s, f.Reviewer)
		case IDMergeQueue:
			mergeQueue(&s, f.Merge)
		case IDLocalCI, IDPRWatcher:
			s.State = StateOn
		case IDLaneSync:
			s.State = StateNotSetUp
			if f.LaneRemotes > 0 {
				s.State = StateOn
				if f.LaneFailing {
					s.State, s.Detail = StateError, DetailLastRunFailed
				}
				if !f.LaneCheckedAt.IsZero() {
					s.LastRun = &f.LaneCheckedAt
				}
			}
		case IDIssueWatch:
			s.State = StateNotSetUp
			if f.IssueWatches > 0 {
				s.State, s.Count = StateOn, f.IssueWatches
			}
		case IDLibrary:
			library(&s, f.Library)
		case IDVoice:
			voice(&s, f.Voice)
		case IDUpdates:
			updates(&s, f.Updates)
		default:
			hookService(&s, f.Hooks[d.id])
		}
		out = append(out, s)
	}
	return out
}

func updates(s *Service, u *UpdateFacts) {
	if u == nil {
		s.State, s.Detail = StateOff, DetailUnreadable
		return
	}
	if u.MultipleHooks {
		s.State, s.Detail = StateError, DetailMultipleUpdateHooks
		return
	}
	if u.Source == "" {
		s.State = StateNotSetUp
		return
	}
	hookService(s, HookFacts{Found: true, Disabled: !u.Enabled, Schedule: &Schedule{EveryMinutes: u.EveryMinutes}, Runs: u.Runs})
	if u.SourceInvalid {
		s.State, s.Detail = StateError, DetailUpdateSourceInvalid
	}
}

func reviews(s *Service, r *ReviewerFacts) {
	if r == nil {
		s.State, s.Detail = StateOff, DetailUnreadable
		return
	}
	s.Switch = &Switch{On: r.Enabled}
	s.Profile, s.Session = r.Profile, r.Session
	if !r.LastVerdictAt.IsZero() {
		s.LastRun = &r.LastVerdictAt
	}
	switch {
	case !r.Enabled:
		s.State = StateOff
	case r.ProfileWaiting:
		s.State, s.Detail = StateWaiting, DetailProfileWaiting
	case r.Failing:
		s.State, s.Detail = StateError, DetailLastRunFailed
	case r.Reviewing:
		s.State, s.Detail, s.Count = StateRunning, DetailWorking, r.Queued
	case r.Queued > 0:
		s.State, s.Detail, s.Count = StateWaiting, DetailQueued, r.Queued
	default:
		s.State = StateIdle
	}
}

func mergeQueue(s *Service, m *MergeFacts) {
	if m == nil {
		s.State, s.Detail = StateOff, DetailUnreadable
		return
	}
	s.Switch = &Switch{On: !m.Paused}
	if !m.LastMerged.IsZero() {
		s.LastRun = &m.LastMerged
	}
	switch {
	case m.Paused:
		s.State, s.Detail = StateOff, DetailPaused
	case m.Running > 0:
		s.State, s.Detail, s.Count = StateRunning, DetailWorking, m.Ready
	case m.Ready > 0:
		s.State, s.Detail, s.Count = StateWaiting, DetailQueued, m.Ready
	default:
		s.State = StateIdle
	}
}

func library(s *Service, l *LibraryFacts) {
	if l == nil {
		s.State, s.Detail = StateOff, DetailUnreadable
		return
	}
	s.Switch = &Switch{On: l.Enabled}
	s.Profile, s.Session, s.NextRun = l.Profile, l.Session, l.NextRun
	if !l.LastRun.IsZero() {
		s.LastRun = &l.LastRun
	}
	switch {
	case !l.Enabled:
		s.State = StateOff
	case l.ProfileWaiting:
		s.State, s.Detail = StateWaiting, DetailProfileWaiting
	case l.Running:
		s.State, s.Detail = StateRunning, DetailWorking
	default:
		s.State = StateIdle
	}
}

func voice(s *Service, v *VoiceFacts) {
	switch {
	case v == nil:
		s.State, s.Detail = StateOff, DetailUnreadable
	case !v.Available:
		s.State, s.Detail = StateOff, DetailUnavailable
	case v.Live:
		s.State, s.Detail = StateRunning, DetailInCall
	default:
		s.State = StateIdle
	}
}

// hookService fills a service that a hook schedules. The newest run by start
// time decides the status, whatever event started it.
func hookService(s *Service, h HookFacts) {
	if !h.Found {
		s.State = StateNotSetUp
		return
	}
	s.Switch = &Switch{On: !h.Disabled}
	s.Schedule = h.Schedule
	s.NextRun = h.NextRun
	var newest *HookRun
	for i := range h.Runs {
		if newest == nil || h.Runs[i].StartedAt.After(newest.StartedAt) {
			newest = &h.Runs[i]
		}
	}
	if newest != nil {
		s.LastRun = &newest.StartedAt
	}
	switch {
	case h.Disabled:
		s.State = StateOff
	case newest == nil:
		s.State, s.Detail = StateWaiting, DetailNoRunYet
	case newest.Result == hooks.ResultPending:
		s.State = StateRunning
	case newest.Result == hooks.ResultError || newest.Result == hooks.ResultFail:
		s.State, s.Detail = StateError, DetailLastRunFailed
	default:
		s.State = StateOn
	}
}
