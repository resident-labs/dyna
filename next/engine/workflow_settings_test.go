package engine

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// settingsRig is a room with root, a lead (a worker that has a child) and a plain worker under root, and a second room whose root has
// no child.
type settingsRig struct {
	workflowCase
	lead, sub, plain contract.SessionID
	other            contract.RoomID
	otherRoot        contract.SessionID
}

func (r settingsRig) as(id contract.SessionID) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: id}
}

func newSettingsRig(t *testing.T) settingsRig {
	t.Helper()
	w := setupWorkflow(t)
	r := settingsRig{workflowCase: w}
	start := func(room contract.RoomID, name string, parent contract.SessionID) contract.SessionID {
		started, problem := w.SessionStart(w.ctx, w.owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent})
		if problem != nil {
			t.Fatal(problem)
		}
		return started.Session
	}
	r.lead = start(w.room, "lead", w.root)
	r.sub = start(w.room, "sub", r.lead)
	r.plain = start(w.room, "plain", w.root)
	created, problem := w.RoomCreate(w.ctx, w.owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	r.other = created.Room
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT id FROM sessions WHERE room=? AND parent IS NULL", r.other).Scan(&r.otherRoot); err != nil {
		t.Fatal(err)
	}
	return r
}

// set changes a scope as the owner does.
func (w workflowCase) set(room contract.RoomID, set contract.WorkflowSettings, clear ...contract.WorkflowSetting) {
	w.t.Helper()
	if _, problem := w.WorkflowSet(w.ctx, w.owner, contract.WorkflowSetRequest{Room: room, Set: set, Clear: clear}); problem != nil {
		w.t.Fatalf("set %+v: %+v", set, problem)
	}
}

// refusedBy checks that a call was refused, naming the setting as its typed cause.
func refusedBy(t *testing.T, what string, problem *contract.Error, setting contract.WorkflowSetting) {
	t.Helper()
	if problem == nil || problem.Code != contract.ErrForbidden || problem.Workflow == nil || problem.Workflow.Setting != setting {
		t.Fatalf("%s: %+v, want a refusal by %s", what, problem, setting)
	}
	if !strings.Contains(problem.Message, string(setting)) {
		t.Fatalf("%s: the message %q does not name %s", what, problem.Message, setting)
	}
}

// boxFor is what a hello tells the box of a crew session that has 16 GiB.
func (r settingsRig) boxFor(session contract.SessionID) *contract.BoxWorkflow {
	r.t.Helper()
	run, err := r.store.SessionWorkflow(r.ctx, session)
	if err != nil {
		r.t.Fatal(err)
	}
	workflow, err := r.boxWorkflow(r.ctx, run, 16<<30)
	if err != nil {
		r.t.Fatal(err)
	}
	return workflow
}

func (w workflowCase) run(caller contract.Principal, room contract.RoomID, script string) *contract.Error {
	_, problem := w.WorkflowRun(w.ctx, caller, contract.WorkflowRunRequest{Room: room, Script: script})
	return problem
}

func TestAnySessionStartsAWorkflowAndTheRunIsItsChild(t *testing.T) {
	r := newSettingsRig(t)
	started, problem := r.WorkflowRun(r.ctx, r.as(r.plain), contract.WorkflowRunRequest{Script: workflowScript})
	if problem != nil {
		t.Fatalf("a worker with no children: %+v", problem)
	}
	var parent contract.SessionID
	if err := r.store.Read().QueryRowContext(r.ctx, "SELECT parent FROM sessions WHERE id=?", started.Session).Scan(&parent); err != nil || parent != r.plain {
		t.Fatalf("the run's parent is %q, %v; want %s", parent, err, r.plain)
	}
	// A crew's workers hold its identity, and workflow() is how a script nests, so a crew starts no run of its own.
	if problem := r.run(r.as(started.Session), r.room, workflowScript); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a crew: %+v", problem)
	}
}

func TestWhoMayStartAWorkflow(t *testing.T) {
	const yes, no = true, false
	cases := []struct {
		starters                                      contract.WorkflowStarters
		owner, root, lead, sub, plain, rootWithoutKid bool
	}{
		{contract.WorkflowStartAny, yes, yes, yes, yes, yes, yes},
		{contract.WorkflowStartLeads, yes, yes, yes, no, no, yes},
		{contract.WorkflowStartRoot, yes, yes, no, no, no, yes},
		{contract.WorkflowStartOff, yes, no, no, no, no, no},
	}
	for _, c := range cases {
		t.Run(string(c.starters), func(t *testing.T) {
			r := newSettingsRig(t)
			r.set("", contract.WorkflowSettings{Start: c.starters})
			for _, who := range []struct {
				name    string
				caller  contract.Principal
				room    contract.RoomID
				allowed bool
			}{
				{"owner", r.owner, r.room, c.owner},
				{"root", r.as(r.root), r.room, c.root},
				{"a lead, which has a child", r.as(r.lead), r.room, c.lead},
				{"a worker under the lead", r.as(r.sub), r.room, c.sub},
				{"a worker with no child", r.as(r.plain), r.room, c.plain},
				{"a root with no child", r.as(r.otherRoot), r.other, c.rootWithoutKid},
			} {
				problem := r.run(who.caller, who.room, workflowScript)
				if who.allowed && problem != nil {
					t.Fatalf("%s may start under %s: %+v", who.name, c.starters, problem)
				}
				if !who.allowed {
					refusedBy(t, who.name+" under "+string(c.starters), problem, contract.WorkflowSettingStart)
				}
			}
		})
	}
}

func TestOnlyTheOwnerReadsAndChangesWorkflowSettings(t *testing.T) {
	r := newSettingsRig(t)
	root := r.as(r.root)
	if _, problem := r.WorkflowSet(r.ctx, root, contract.WorkflowSetRequest{Set: contract.WorkflowSettings{Start: contract.WorkflowStartOff}}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("root sets: %+v", problem)
	}
	if _, problem := r.WorkflowSettings(r.ctx, root, contract.WorkflowSettingsRequest{}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("root reads: %+v", problem)
	}
	got, problem := r.WorkflowSettings(r.ctx, r.owner, contract.WorkflowSettingsRequest{})
	if problem != nil || got.Global.Effective.Start != contract.WorkflowStartAny {
		t.Fatalf("settings %+v: %+v", got.Global, problem)
	}
	if len(got.Settings) != len(contract.AllWorkflowSettings) || !reflect.DeepEqual(got.Defaults, contract.WorkflowDefaults) {
		t.Fatalf("settings with their help, and the defaults: %+v", got)
	}
}

func TestAWorkflowSettingIsCheckedWhenItIsSet(t *testing.T) {
	r := newSettingsRig(t)
	negative, tooShort := -1, 0
	overCount, overTimeout := contract.MaxWorkflowCount+1, contract.MaxWorkflowTimeout+1
	for name, request := range map[string]contract.WorkflowSetRequest{
		"a starter":              {Set: contract.WorkflowSettings{Start: "nobody"}},
		"a negative run limit":   {Set: contract.WorkflowSettings{MaxRuns: &negative}},
		"a negative worker cap":  {Set: contract.WorkflowSettings{MaxWorkers: &negative}},
		"a timeout under 1s":     {Set: contract.WorkflowSettings{Timeout: &tooShort}},
		"too many runs":          {Set: contract.WorkflowSettings{MaxRuns: &overCount}},
		"too many workers":       {Set: contract.WorkflowSettings{MaxWorkers: &overCount}},
		"a timeout over the cap": {Set: contract.WorkflowSettings{Timeout: &overTimeout}},
		"an unknown size":        {Set: contract.WorkflowSettings{Size: "huge"}},
		"an unknown profile":     {Set: contract.WorkflowSettings{Profiles: []contract.ProfileName{"nope"}}},
		"a setting in both":      {Set: contract.WorkflowSettings{Start: contract.WorkflowStartRoot}, Clear: []contract.WorkflowSetting{contract.WorkflowSettingStart}},
		"a setting that is none": {Clear: []contract.WorkflowSetting{"colour"}},
		"no change":              {},
		"a room that is not":     {Room: "room_none", Set: contract.WorkflowSettings{Start: contract.WorkflowStartRoot}},
	} {
		if _, problem := r.WorkflowSet(r.ctx, r.owner, request); problem == nil || problem.Code == contract.ErrForbidden {
			t.Fatalf("%s: %+v", name, problem)
		}
	}
	// The caps themselves are allowed, and the room-level values read back.
	atCount, atTimeout := contract.MaxWorkflowCount, contract.MaxWorkflowTimeout
	if _, problem := r.WorkflowSet(r.ctx, r.owner, contract.WorkflowSetRequest{Room: r.other, Set: contract.WorkflowSettings{MaxRuns: &atCount, MaxWorkers: &atCount, Timeout: &atTimeout}}); problem != nil {
		t.Fatalf("the caps themselves: %+v", problem)
	}
	r.set(r.other, contract.WorkflowSettings{}, contract.WorkflowSettingMaxRuns, contract.WorkflowSettingMaxWorkers, contract.WorkflowSettingTimeout)
	got, problem := r.WorkflowSettings(r.ctx, r.owner, contract.WorkflowSettingsRequest{})
	if problem != nil || !reflect.DeepEqual(got.Global.Effective, contract.WorkflowDefaults) || len(got.Rooms) != 2 {
		t.Fatalf("a refused change left %+v: %+v", got.Global, problem)
	}
}

// A room's value wins over the global one, which wins over the built-in default; clearing a value falls back one level.
func TestARoomSettingOverridesTheGlobalOne(t *testing.T) {
	r := newSettingsRig(t)
	effective := func(room contract.RoomID) contract.WorkflowEffective {
		t.Helper()
		got, problem := r.WorkflowSettings(r.ctx, r.owner, contract.WorkflowSettingsRequest{Room: room})
		if problem != nil || len(got.Rooms) != 1 {
			t.Fatalf("%+v: %+v", got, problem)
		}
		return got.Rooms[0].Effective
	}
	two, nine := 2, 9
	r.set("", contract.WorkflowSettings{Start: contract.WorkflowStartLeads, MaxRuns: &nine})
	if got := effective(r.room); got.Start != contract.WorkflowStartLeads || got.MaxRuns != 9 {
		t.Fatalf("the global value reaches a room with no override: %+v", got)
	}
	r.set(r.room, contract.WorkflowSettings{Start: contract.WorkflowStartRoot, MaxRuns: &two})
	if got := effective(r.room); got.Start != contract.WorkflowStartRoot || got.MaxRuns != 2 {
		t.Fatalf("the room's value wins: %+v", got)
	}
	if got := effective(r.other); got.Start != contract.WorkflowStartLeads || got.MaxRuns != 9 {
		t.Fatalf("another room keeps the global value: %+v", got)
	}
	// The override holds in what is enforced, not only in what is read.
	refusedBy(t, "a lead in the overriding room", r.run(r.as(r.lead), r.room, workflowScript), contract.WorkflowSettingStart)
	if problem := r.run(r.as(r.otherRoot), r.other, workflowScript); problem != nil {
		t.Fatalf("a root in the room that inherits leads only: %+v", problem)
	}
	r.set(r.room, contract.WorkflowSettings{}, contract.WorkflowSettingStart)
	if got := effective(r.room); got.Start != contract.WorkflowStartLeads || got.MaxRuns != 2 {
		t.Fatalf("clearing one value keeps the others: %+v", got)
	}
	r.set("", contract.WorkflowSettings{}, contract.WorkflowSettingStart)
	if got := effective(r.other); got.Start != contract.WorkflowStartAny || got.MaxRuns != 9 {
		t.Fatalf("clearing a global value returns to the built-in default: %+v", got)
	}
	// A room's own value of 0 is a value: no limit, over a global limit.
	zero := 0
	r.set(r.room, contract.WorkflowSettings{MaxRuns: &zero})
	if got := effective(r.room); got.MaxRuns != 0 {
		t.Fatalf("a zero override: %+v", got)
	}
}

func TestAnOffSwitchRefusesItsFeatureAndNamesItself(t *testing.T) {
	for _, feature := range contract.AllWorkflowFeatures {
		t.Run(string(feature), func(t *testing.T) {
			r := newSettingsRig(t)
			started, crew := r.start(r.owner, workflowScript)
			off := false
			set := contract.WorkflowSettings{}
			switch feature {
			case contract.WorkflowFeatureWorktree:
				set.Worktree = &off
			case contract.WorkflowFeatureMerge:
				set.Merge = &off
			case contract.WorkflowFeatureRun:
				set.Run = &off
			case contract.WorkflowFeatureWorkflow:
				set.Workflow = &off
			case contract.WorkflowFeaturePost:
				set.Post = &off
			case contract.WorkflowFeatureResume:
				set.Resume = &off
			}
			r.set(r.room, set)
			setting := contract.WorkflowSetting(feature)
			if feature == contract.WorkflowFeatureResume {
				_, problem := r.WorkflowRun(r.ctx, r.owner, contract.WorkflowRunRequest{Room: r.room, Script: workflowScript, Resume: started.Run})
				refusedBy(t, "resume", problem, setting)
				return
			}
			_, problem := r.WorkflowStepStart(r.ctx, crew, contract.WorkflowStepStartRequest{Key: "k-" + string(feature), Kind: contract.WorkflowStepAgent, Name: "n", Detail: "d", Feature: feature})
			refusedBy(t, string(feature), problem, setting)
			// Every other feature, and a step no switch gates, still starts.
			for _, other := range contract.AllWorkflowFeatures {
				if other == feature || other == contract.WorkflowFeatureResume {
					continue
				}
				if _, problem := r.WorkflowStepStart(r.ctx, crew, contract.WorkflowStepStartRequest{Key: "ok-" + string(other), Kind: contract.WorkflowStepAgent, Name: "n", Detail: "d", Feature: other}); problem != nil {
					t.Fatalf("%s, with only %s off: %+v", other, feature, problem)
				}
			}
			if _, problem := r.WorkflowStepStart(r.ctx, crew, contract.WorkflowStepStartRequest{Key: "plain", Kind: contract.WorkflowStepAgent, Name: "n", Detail: "d"}); problem != nil {
				t.Fatalf("a step no switch gates: %+v", problem)
			}
			// Another room is not touched by this room's switch.
			if _, other := r.start(r.owner, workflowScript); other.Session == "" {
				t.Fatal("no crew")
			}
		})
	}
}

// The global switch reaches rooms that do not set it, and a room's own value overrides it.
func TestASwitchFollowsTheOverrideOrder(t *testing.T) {
	r := newSettingsRig(t)
	_, crew := r.start(r.owner, workflowScript)
	off, on := false, true
	calls := 0
	call := func() *contract.Error {
		calls++
		_, problem := r.WorkflowStepStart(r.ctx, crew, contract.WorkflowStepStartRequest{Key: fmt.Sprint("k", calls), Kind: contract.WorkflowStepCommand, Name: "n", Detail: "d", Feature: contract.WorkflowFeatureRun})
		return problem
	}
	r.set("", contract.WorkflowSettings{Run: &off})
	refusedBy(t, "global off", call(), contract.WorkflowSetting(contract.WorkflowFeatureRun))
	r.set(r.room, contract.WorkflowSettings{Run: &on})
	if problem := call(); problem != nil {
		t.Fatalf("the room's on over the global off: %+v", problem)
	}
	r.set(r.room, contract.WorkflowSettings{}, contract.WorkflowSetting(contract.WorkflowFeatureRun))
	refusedBy(t, "override cleared", call(), contract.WorkflowSetting(contract.WorkflowFeatureRun))
}

func TestRunsGoingAtOnceAreLimitedPerRoom(t *testing.T) {
	r := newSettingsRig(t)
	one := 1
	r.set(r.room, contract.WorkflowSettings{MaxRuns: &one})
	first, _ := r.start(r.owner, workflowScript)
	refusedBy(t, "a second run", r.run(r.as(r.plain), r.room, workflowScript), contract.WorkflowSettingMaxRuns)
	refusedBy(t, "the owner's second run", r.run(r.owner, r.room, workflowScript), contract.WorkflowSettingMaxRuns)
	if problem := r.run(r.as(r.otherRoot), r.other, workflowScript); problem != nil {
		t.Fatalf("another room has its own count: %+v", problem)
	}
	// A run that ended no longer counts; closing its crew ends it.
	if _, problem := r.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: first.Session}); problem != nil {
		t.Fatal(problem)
	}
	if problem := r.run(r.owner, r.room, workflowScript); problem != nil {
		t.Fatalf("after run %s ended: %+v", first.Run, problem)
	}
	two := 2
	r.set(r.room, contract.WorkflowSettings{MaxRuns: &two})
	if problem := r.run(r.owner, r.room, workflowScript); problem != nil {
		t.Fatalf("a limit of 2 with one going: %+v", problem)
	}
	refusedBy(t, "a third run", r.run(r.owner, r.room, workflowScript), contract.WorkflowSettingMaxRuns)
}

func TestWorkersPerRunAreLimitedOnTopOfTheBoxsMemory(t *testing.T) {
	r := newSettingsRig(t)
	large := "export const meta = { name: 'big', profiles: ['fast'], size: 'large' }\n"
	workers := func() int {
		t.Helper()
		started, _ := r.start(r.owner, large)
		return r.boxFor(started.Session).Workers
	}
	unlimited := workers()
	if unlimited < 4 {
		t.Fatalf("a large box allows %d workers", unlimited)
	}
	three := 3
	r.set("", contract.WorkflowSettings{MaxWorkers: &three})
	if got := workers(); got != 3 {
		t.Fatalf("a cap of 3: %d", got)
	}
	huge := unlimited + 50
	r.set(r.room, contract.WorkflowSettings{MaxWorkers: &huge})
	if got := workers(); got != unlimited {
		t.Fatalf("a cap above the box's memory: %d, want %d", got, unlimited)
	}
}

func TestTheProfilesAWorkflowMayNameAreLimited(t *testing.T) {
	r := newSettingsRig(t)
	r.set(r.room, contract.WorkflowSettings{Profiles: []contract.ProfileName{"fast"}})
	refusedBy(t, "a script naming deep", r.run(r.owner, r.room, workflowScript), contract.WorkflowSettingProfiles)
	only := "export const meta = { name: 'one', profiles: ['fast'] }\n"
	started, problem := r.WorkflowRun(r.ctx, r.owner, contract.WorkflowRunRequest{Room: r.room, Script: only})
	if problem != nil {
		t.Fatalf("a script naming only fast: %+v", problem)
	}
	refusedBy(t, "a reload to deep", func() *contract.Error {
		_, problem := r.WorkflowReload(r.ctx, r.owner, contract.WorkflowReloadRequest{Run: started.Run, Script: "export const meta = { name: 'one', profiles: ['fast', 'deep'] }\n"})
		return problem
	}(), contract.WorkflowSettingProfiles)
	if problem := r.run(r.owner, r.other, workflowScript); problem != nil {
		t.Fatalf("another room allows every profile: %+v", problem)
	}
}

func TestTheDefaultSizeAndTimeoutApplyWhereTheScriptSetsNone(t *testing.T) {
	r := newSettingsRig(t)
	size := func(script string) (contract.BoxSizeName, int) {
		t.Helper()
		started, problem := r.WorkflowRun(r.ctx, r.owner, contract.WorkflowRunRequest{Room: r.room, Script: script})
		if problem != nil {
			t.Fatal(problem)
		}
		var got contract.BoxSizeName
		if err := r.store.Read().QueryRowContext(r.ctx, "SELECT size FROM boxes WHERE session=?", started.Session).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got, r.boxFor(started.Session).Timeout
	}
	if got, timeout := size(workflowScript); got != contract.BoxSizeMedium && got != contract.BoxSizeAgent || timeout != contract.DefaultWorkflowTimeout {
		t.Fatalf("with no settings: size %q, timeout %d", got, timeout)
	}
	seconds := 90
	r.set("", contract.WorkflowSettings{Size: contract.BoxSizeLarge, Timeout: &seconds})
	if got, timeout := size(workflowScript); got != contract.BoxSizeLarge || timeout != 90 {
		t.Fatalf("with defaults set: size %q, timeout %d", got, timeout)
	}
	if got, _ := size("export const meta = { name: 'small', profiles: ['fast'], size: 'small' }\n"); got != contract.BoxSizeSmall {
		t.Fatalf("the script's own size wins: %q", got)
	}
}

func TestAReloadIsLimitedToTheStarterItsAncestorsAndTheOwner(t *testing.T) {
	r := newSettingsRig(t)
	// lead starts a run: its parent is lead, whose parent is root. sub, plain and the other room's root are not above it.
	started, problem := r.WorkflowRun(r.ctx, r.as(r.lead), contract.WorkflowRunRequest{Script: workflowScript})
	if problem != nil {
		t.Fatal(problem)
	}
	reload := func(caller contract.Principal) *contract.Error {
		_, problem := r.WorkflowReload(r.ctx, caller, contract.WorkflowReloadRequest{Run: started.Run, Script: workflowScript})
		return problem
	}
	for name, caller := range map[string]contract.Principal{"its sibling's child": r.as(r.sub), "an unrelated worker": r.as(r.plain), "the crew": r.as(started.Session)} {
		if problem := reload(caller); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("%s reloads: %+v", name, problem)
		}
	}
	for name, caller := range map[string]contract.Principal{"the starter": r.as(r.lead), "an ancestor of the starter": r.as(r.root), "the owner": r.owner} {
		if problem := reload(caller); problem != nil {
			t.Fatalf("%s reloads: %+v", name, problem)
		}
	}
}

func TestTheWorkflowSectionShowsOnlyAFarFromDefaultRule(t *testing.T) {
	r := newSettingsRig(t)
	section := func(who contract.SessionID) (string, bool) {
		t.Helper()
		got, problem := r.ContextSnapshot(r.ctx, r.as(who), contract.ContextSnapshotRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, sec := range got.Sections {
			if sec.Key == contract.ContextWorkflows {
				return sec.Text, true
			}
		}
		return "", false
	}
	if text, ok := section(r.plain); ok {
		t.Fatalf("at the defaults: %q", text)
	}
	off := false
	three := 3
	r.set("", contract.WorkflowSettings{MaxRuns: &three, Size: contract.BoxSizeLarge})
	if text, ok := section(r.plain); ok {
		t.Fatalf("limits and defaults are not shown, a refusal names them: %q", text)
	}
	r.set(r.room, contract.WorkflowSettings{Start: contract.WorkflowStartLeads, Merge: &off, Post: &off})
	want := "Workflows: started by leads (root and sessions that have started a child)\nOff: merge(), post()"
	for _, who := range []contract.SessionID{r.root, r.lead, r.plain} {
		if text, ok := section(who); !ok || text != want {
			t.Fatalf("session %s: %q, %v", who, text, ok)
		}
	}
	if text, ok := section(r.otherRoot); ok {
		t.Fatalf("another room's: %q", text)
	}
	r.set(r.room, contract.WorkflowSettings{Start: contract.WorkflowStartOff})
	if text, _ := section(r.plain); !strings.HasPrefix(text, "Workflows: no session starts one") {
		t.Fatalf("off: %q", text)
	}
	r.set(r.room, contract.WorkflowSettings{}, contract.WorkflowSettingStart, contract.WorkflowSetting(contract.WorkflowFeatureMerge), contract.WorkflowSetting(contract.WorkflowFeaturePost))
	if text, ok := section(r.plain); ok {
		t.Fatalf("back at the defaults: %q", text)
	}
	one, err := r.ContextSnapshot(r.ctx, r.as(r.plain), contract.ContextSnapshotRequest{Section: contract.ContextWorkflows})
	if err != nil || !slices.Equal(one.Sections, []contract.ContextSection{}) && len(one.Sections) != 0 {
		t.Fatalf("one section: %+v %v", one, err)
	}
}
