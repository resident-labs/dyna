package contract

const (
	// MethodWorkflowSettings reads the built-in defaults, the global values and each room's overrides. It is the owner's.
	MethodWorkflowSettings Method = "workflow.settings"
	// MethodWorkflowSet changes the global values or one room's overrides. It is the owner's.
	MethodWorkflowSet Method = "workflow.set"
)

// WorkflowStarters says who may start a workflow in a room. The owner starts one whatever it says.
type WorkflowStarters string

const (
	WorkflowStartAny   WorkflowStarters = "any"   // every session
	WorkflowStartLeads WorkflowStarters = "leads" // the room's root and sessions that have ever started a child
	WorkflowStartRoot  WorkflowStarters = "root"  // the room's root
	WorkflowStartOff   WorkflowStarters = "off"   // no session
)

// AllWorkflowStarters lists the starters, from the widest to the narrowest.
var AllWorkflowStarters = []WorkflowStarters{WorkflowStartAny, WorkflowStartLeads, WorkflowStartRoot, WorkflowStartOff}

// WorkflowFeature is a part of a script's API that the owner may turn off, and the setting of the same name decides it.
type WorkflowFeature string

const (
	WorkflowFeatureWorktree WorkflowFeature = "worktree" // agent() with worktree: true
	WorkflowFeatureMerge    WorkflowFeature = "merge"    // merge()
	WorkflowFeatureRun      WorkflowFeature = "run"      // run() shell commands
	WorkflowFeatureWorkflow WorkflowFeature = "workflow" // workflow(), a script that starts another
	WorkflowFeaturePost     WorkflowFeature = "post"     // post() to the parent
	WorkflowFeatureResume   WorkflowFeature = "resume"   // workflow.run with resume
)

// AllWorkflowFeatures lists the switches in the order the settings show them.
var AllWorkflowFeatures = []WorkflowFeature{WorkflowFeatureWorktree, WorkflowFeatureMerge, WorkflowFeatureRun, WorkflowFeatureWorkflow, WorkflowFeaturePost, WorkflowFeatureResume}

// WorkflowSetting names one workflow setting: the ones below, and each WorkflowFeature, whose setting has the feature's name.
type WorkflowSetting string

const (
	WorkflowSettingStart      WorkflowSetting = "start"
	WorkflowSettingMaxRuns    WorkflowSetting = "maxRuns"
	WorkflowSettingMaxWorkers WorkflowSetting = "maxWorkers"
	WorkflowSettingProfiles   WorkflowSetting = "profiles"
	WorkflowSettingSize       WorkflowSetting = "size"
	WorkflowSettingTimeout    WorkflowSetting = "timeout"
	// The switches, spelled out so the generated web type names every setting the engine returns and takes.
	WorkflowSettingWorktree WorkflowSetting = "worktree"
	WorkflowSettingMerge    WorkflowSetting = "merge"
	WorkflowSettingRun      WorkflowSetting = "run"
	WorkflowSettingWorkflow WorkflowSetting = "workflow"
	WorkflowSettingPost     WorkflowSetting = "post"
	WorkflowSettingResume   WorkflowSetting = "resume"
)

// WorkflowSettingInfo is a setting with its one line of help, which the CLI and Settings share.
type WorkflowSettingInfo struct {
	Setting WorkflowSetting `json:"setting"`
	Help    string          `json:"help"`
}

// AllWorkflowSettings lists every setting in the order the settings show them, with its one line of help.
var AllWorkflowSettings = []WorkflowSettingInfo{
	{WorkflowSettingStart, "Who may start a workflow: any session, leads (the root and sessions that have started a child), root, or off. The owner always may."},
	{WorkflowSettingMaxRuns, "The most workflow runs going at once in the room; 0 is no limit."},
	{WorkflowSettingMaxWorkers, "The most workers one run has at once, on top of its box's memory and each profile's maxConcurrent; 0 is no limit. A run's box reads it when it starts."},
	{WorkflowSettingProfiles, "The profiles a script's meta.profiles may name; none set means every profile."},
	{WorkflowSettingSize, "The box size of a run whose script sets no meta.size; none set means the engine's choice."},
	{WorkflowSettingTimeout, "Seconds a worker or command may run when agent() or run() sets no timeout."},
	{WorkflowSettingWorktree, "On or off: agent() with worktree: true, a worker in its own git worktree and branch."},
	{WorkflowSettingMerge, "On or off: merge(), which merges worktree results into one run branch."},
	{WorkflowSettingRun, "On or off: run(), shell commands in the run's box."},
	{WorkflowSettingWorkflow, "On or off: workflow(), a script that starts another script."},
	{WorkflowSettingPost, "On or off: post(), a message from the run to its parent."},
	{WorkflowSettingResume, "On or off: starting a run with resume, which keeps an ended run's finished steps."},
}

// DefaultWorkflowTimeout is how long a worker runs when neither the script nor the settings say: 5 hours, in seconds.
const DefaultWorkflowTimeout = 5 * 60 * 60

// MaxWorkflowTimeout is the longest default timeout the owner may set: 30 days in seconds, far below where seconds as a duration overflow.
const MaxWorkflowTimeout = 30 * 24 * 60 * 60

// MaxWorkflowCount is the most runs per room or workers per run the owner may set; 0 stays no limit.
const MaxWorkflowCount = 10000

// WorkflowSettings is the values one scope sets, the global one or a room's. A value that is not set is inherited: a room's
// from the global scope, and the global scope's from WorkflowDefaults. Profiles that is empty is not set.
type WorkflowSettings struct {
	Start      WorkflowStarters `json:"start,omitempty"`
	MaxRuns    *int             `json:"maxRuns,omitempty"`
	MaxWorkers *int             `json:"maxWorkers,omitempty"`
	Profiles   []ProfileName    `json:"profiles,omitempty"`
	Size       BoxSizeName      `json:"size,omitempty"`
	Timeout    *int             `json:"timeout,omitempty"`
	Worktree   *bool            `json:"worktree,omitempty"`
	Merge      *bool            `json:"merge,omitempty"`
	Run        *bool            `json:"run,omitempty"`
	Workflow   *bool            `json:"workflow,omitempty"`
	Post       *bool            `json:"post,omitempty"`
	Resume     *bool            `json:"resume,omitempty"`
}

// WorkflowEffective is every setting with its value in force. Profiles empty means every profile, and Size empty the engine's
// choice. MaxRuns and MaxWorkers of 0 mean no limit.
type WorkflowEffective struct {
	Start      WorkflowStarters `json:"start"`
	MaxRuns    int              `json:"maxRuns"`
	MaxWorkers int              `json:"maxWorkers"`
	Profiles   []ProfileName    `json:"profiles,omitempty"`
	Size       BoxSizeName      `json:"size,omitempty"`
	Timeout    int              `json:"timeout"`
	Worktree   bool             `json:"worktree"`
	Merge      bool             `json:"merge"`
	Run        bool             `json:"run"`
	Workflow   bool             `json:"workflow"`
	Post       bool             `json:"post"`
	Resume     bool             `json:"resume"`
}

// WorkflowDefaults is what applies where no scope sets a value: any session starts a workflow, no limit, every profile, and
// every feature on, as before the settings existed.
var WorkflowDefaults = WorkflowEffective{Start: WorkflowStartAny, Timeout: DefaultWorkflowTimeout, Worktree: true, Merge: true, Run: true, Workflow: true, Post: true, Resume: true}

// Over is the values in force when these settings are set over base.
func (s WorkflowSettings) Over(base WorkflowEffective) WorkflowEffective {
	if s.Start != "" {
		base.Start = s.Start
	}
	if s.MaxRuns != nil {
		base.MaxRuns = *s.MaxRuns
	}
	if s.MaxWorkers != nil {
		base.MaxWorkers = *s.MaxWorkers
	}
	if len(s.Profiles) > 0 {
		base.Profiles = s.Profiles
	}
	if s.Size != "" {
		base.Size = s.Size
	}
	if s.Timeout != nil {
		base.Timeout = *s.Timeout
	}
	for feature, value := range map[WorkflowFeature]*bool{WorkflowFeatureWorktree: s.Worktree, WorkflowFeatureMerge: s.Merge, WorkflowFeatureRun: s.Run, WorkflowFeatureWorkflow: s.Workflow, WorkflowFeaturePost: s.Post, WorkflowFeatureResume: s.Resume} {
		if value != nil {
			base.Switch(feature, *value)
		}
	}
	return base
}

// Switch sets one feature's switch.
func (e *WorkflowEffective) Switch(feature WorkflowFeature, on bool) {
	switch feature {
	case WorkflowFeatureWorktree:
		e.Worktree = on
	case WorkflowFeatureMerge:
		e.Merge = on
	case WorkflowFeatureRun:
		e.Run = on
	case WorkflowFeatureWorkflow:
		e.Workflow = on
	case WorkflowFeaturePost:
		e.Post = on
	case WorkflowFeatureResume:
		e.Resume = on
	}
}

// Allows says whether a feature's switch is on. A name that is not a feature is not allowed.
func (e WorkflowEffective) Allows(feature WorkflowFeature) bool {
	switch feature {
	case WorkflowFeatureWorktree:
		return e.Worktree
	case WorkflowFeatureMerge:
		return e.Merge
	case WorkflowFeatureRun:
		return e.Run
	case WorkflowFeatureWorkflow:
		return e.Workflow
	case WorkflowFeaturePost:
		return e.Post
	case WorkflowFeatureResume:
		return e.Resume
	}
	return false
}

// WorkflowRefused is the cause of a call that a workflow setting refused: the setting's name, so a caller reads it and no
// message.
type WorkflowRefused struct {
	Setting WorkflowSetting `json:"setting"`
}

// WorkflowSettingsRequest reads the settings. A room narrows the answer to that room.
type WorkflowSettingsRequest struct {
	Room RoomID `json:"room,omitempty"`
}

// WorkflowScope is the values one scope sets, with the values in force there. Room is empty for the global scope, which has no
// name.
type WorkflowScope struct {
	Room      RoomID            `json:"room,omitempty"`
	Name      string            `json:"name,omitempty"`
	Set       WorkflowSettings  `json:"set"`
	Effective WorkflowEffective `json:"effective"`
}

// WorkflowSettingsResponse holds the settings with their help, the built-in defaults, the global scope and each room's scope,
// every open room by name or the one asked for.
type WorkflowSettingsResponse struct {
	Settings []WorkflowSettingInfo `json:"settings"`
	Defaults WorkflowEffective     `json:"defaults"`
	Global   WorkflowScope         `json:"global"`
	Rooms    []WorkflowScope       `json:"rooms"`
}

// WorkflowSetRequest changes one scope: a room's, or the global one when Room is empty. Set takes the values to set and Clear
// the settings to return to inheriting. A setting in both is refused.
type WorkflowSetRequest struct {
	Room  RoomID            `json:"room,omitempty"`
	Set   WorkflowSettings  `json:"set"`
	Clear []WorkflowSetting `json:"clear,omitempty"`
}
type WorkflowSetResponse struct{}

// Named lists the settings these values set.
func (s WorkflowSettings) Named() []WorkflowSetting {
	var named []WorkflowSetting
	for _, field := range []struct {
		setting WorkflowSetting
		set     bool
	}{
		{WorkflowSettingStart, s.Start != ""}, {WorkflowSettingMaxRuns, s.MaxRuns != nil}, {WorkflowSettingMaxWorkers, s.MaxWorkers != nil},
		{WorkflowSettingProfiles, len(s.Profiles) > 0}, {WorkflowSettingSize, s.Size != ""}, {WorkflowSettingTimeout, s.Timeout != nil},
		{WorkflowSetting(WorkflowFeatureWorktree), s.Worktree != nil}, {WorkflowSetting(WorkflowFeatureMerge), s.Merge != nil},
		{WorkflowSetting(WorkflowFeatureRun), s.Run != nil}, {WorkflowSetting(WorkflowFeatureWorkflow), s.Workflow != nil},
		{WorkflowSetting(WorkflowFeaturePost), s.Post != nil}, {WorkflowSetting(WorkflowFeatureResume), s.Resume != nil},
	} {
		if field.set {
			named = append(named, field.setting)
		}
	}
	return named
}

// Changed is these values with the ones set in change put over them and the settings in clear removed. A name in clear that is
// no setting is ignored: the caller checks names against AllWorkflowSettings.
func (s WorkflowSettings) Changed(change WorkflowSettings, clear []WorkflowSetting) WorkflowSettings {
	if change.Start != "" {
		s.Start = change.Start
	}
	if change.MaxRuns != nil {
		s.MaxRuns = change.MaxRuns
	}
	if change.MaxWorkers != nil {
		s.MaxWorkers = change.MaxWorkers
	}
	if len(change.Profiles) > 0 {
		s.Profiles = change.Profiles
	}
	if change.Size != "" {
		s.Size = change.Size
	}
	if change.Timeout != nil {
		s.Timeout = change.Timeout
	}
	if change.Worktree != nil {
		s.Worktree = change.Worktree
	}
	if change.Merge != nil {
		s.Merge = change.Merge
	}
	if change.Run != nil {
		s.Run = change.Run
	}
	if change.Workflow != nil {
		s.Workflow = change.Workflow
	}
	if change.Post != nil {
		s.Post = change.Post
	}
	if change.Resume != nil {
		s.Resume = change.Resume
	}
	for _, setting := range clear {
		switch setting {
		case WorkflowSettingStart:
			s.Start = ""
		case WorkflowSettingMaxRuns:
			s.MaxRuns = nil
		case WorkflowSettingMaxWorkers:
			s.MaxWorkers = nil
		case WorkflowSettingProfiles:
			s.Profiles = nil
		case WorkflowSettingSize:
			s.Size = ""
		case WorkflowSettingTimeout:
			s.Timeout = nil
		case WorkflowSetting(WorkflowFeatureWorktree):
			s.Worktree = nil
		case WorkflowSetting(WorkflowFeatureMerge):
			s.Merge = nil
		case WorkflowSetting(WorkflowFeatureRun):
			s.Run = nil
		case WorkflowSetting(WorkflowFeatureWorkflow):
			s.Workflow = nil
		case WorkflowSetting(WorkflowFeaturePost):
			s.Post = nil
		case WorkflowSetting(WorkflowFeatureResume):
			s.Resume = nil
		}
	}
	return s
}
