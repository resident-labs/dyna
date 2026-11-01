package main

import (
	"reflect"
	"slices"

	account "dyna-agent/internal/account/store"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/machines"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc/devicestore"
)

type fieldInventory struct {
	Type    string   `json:"type"`
	Carried []string `json:"carried"`
	Dropped []string `json:"dropped"`
	Handoff []string `json:"handoff"`
	Gap     []string `json:"gap"`
}

// inventory enumerates the source types so additions cannot silently vanish
// from the report. Fields without an explicit decision are gaps.
func inventory() []fieldInventory {
	specs := []struct {
		name             string
		typ              reflect.Type
		carried, dropped []string
	}{
		{"Rule", reflect.TypeOf(goals.Rule{}), []string{"Text"}, []string{"ID", "Setter", "SetterName", "CreatedAt", "UpdatedAt", "Edits"}},
		{"Goal", reflect.TypeOf(goals.Goal{}), []string{"ID", "Room", "Text", "Detail", "Amendments", "Setter", "CreatedAt"}, []string{"Holders", "Shared", "NextItem", "ItemsClosed", "State", "UpdatedAt", "Edits", "Done", "ExpediteUntil", "LegacyOwnerHeld"}},
		{"Item", reflect.TypeOf(goals.Item{}), []string{"ID", "Text", "State", "Assignee"}, []string{"UpdatedAt"}},
		{"Question", reflect.TypeOf(questions.Question{}), []string{"ID", "Room", "Asker", "AskerKind", "Text", "Options", "Meanwhile", "AskedAt", "Kind", "Terminal"}, []string{"To", "Post", "State", "Answer", "Withdrawn", "GroupKey", "AskedTurn", "AnswerDelivered", "Human"}},
		{"TerminalRequest", reflect.TypeOf(questions.TerminalRequest{}), []string{"Argv", "Cwd", "Reason"}, []string{"Stage", "Holder", "TermID", "ExitCode", "SharedLines"}},
		{"Issue", reflect.TypeOf(room.Issue{}), []string{"ID", "Number", "Title", "File", "Line", "Filer", "FiledAt", "GoalID", "Assignee", "GitHubURL"}, []string{"State", "Reason", "FixPR", "InProgress", "GoalShared", "GoalHolder", "ChangedBy", "ChangedAt"}},
		{"Machine", reflect.TypeOf(machines.Machine{}), []string{"Name", "SSH", "Rooms", "Labels"}, []string{"ID", "Enabled", "Local", "Created", "Updated", "Health", "Capabilities", "Load", "LastProbe", "LastSeen", "ProbeError", "Resources", "Mounts", "Devices", "HostChecksGrants", "ContainerSocket", "SSHConnectTimeout", "CheckTmp", "MaxIOHeavy", "QuickSlots", "MaxDiskPercent"}},
		{"Profile", reflect.TypeOf(profile.Profile{}), []string{"Name", "Description", "Model", "Models", "Fallback", "Taste", "Intelligence", "Cost", "Diagnostics", "Compact", "MaxConcurrent", "Default"}, []string{"Harness", "Uses", "Disabled", "RuntimeVersion", "Output", "ExtraArgs", "Env", "TimeoutSec", "Managed", "SafeMode"}},
		{"AccountMetadata", reflect.TypeOf(account.Metadata{}), []string{"Provider", "Label", "RoomID"}, []string{"NativeHome", "Observation", "ID", "Kind", "Harness", "Integration", "Identity", "LabelSource", "NeedsLogin", "RefreshPending", "CreatedAt", "UpdatedAt"}},
		{"Device", reflect.TypeOf(devicestore.Device{}), []string{"Name", "Hash", "Created", "Expires", "Scopes"}, []string{"ID", "LastSeen"}},
		{"Hook", reflect.TypeOf(hooks.Hook{}), nil, []string{"Disabled", "Builtin"}},
		{"TrackedPR", reflect.TypeOf(prstate.Record{}), []string{"Repo", "Number", "Author", "GoalID", "Branch", "Hold"}, []string{"PublishedHead", "PendingVerdict", "ReleaseReviewersAfterReplay", "MergeTrialFailure", "IsDraft", "Labels", "LabelsSeen", "Mergeable", "DefaultBranch", "ChecksAt", "CIBudgetHead", "CIBudgetErrors", "CIBudgetRuns", "CIBudgetNotes", "CIBudgetInputs", "CIBudgetSteps", "CIQueueWait", "CITestPolicy", "CITestReportNote", "CINoJUnitWarned", "CITestHead", "CITestErrors", "CITestFailures", "CITestRuns", "CITestRetries", "CIStaleCleanupHead", "Polling", "RateLimit", "ReviewHistory", "CI", "CIReason", "Checks", "CIKey", "Failures", "CIPaused", "PauseReason", "Error", "Round", "RoundHead", "RoundStarted", "ReviewPublishing", "ReviewerMode", "ReviewerOverride", "PolicyOverride", "Landing", "ChangeID", "Kind", "RepoPath", "BaseRef", "BaseRetargetSeen", "Notes", "Category", "Dependencies", "Fixes", "MergeCommit", "Findings", "RequiredReviewers", "ReviewRounds", "QuotaAccount", "Key", "Host", "URL", "Title", "RoomID", "AuthorSource", "FormerAuthors", "Controller", "Parent", "Dir", "Base", "LookupDir", "Head", "HeadSeen", "HeadRepo", "HeadStale", "HeadVerifiedAt", "State", "ClosedAt", "CloseEpisode", "CICleanupDone", "Watching", "Created", "Checked", "LocalCIConfig", "BotReviews", "Reviewer", "ReviewerName", "Verdict", "Summary", "ReviewID", "StopReason", "FastLand", "CommentTrust", "OwnerLogin", "HeldComments", "AdmittedComments", "CommentDecisions", "Seen", "Notices", "Overlaps", "PendingDrops", "LastReviewer", "LastReviewHead", "ReviewAssignments"}},
		{"Settings", reflect.TypeOf(settings.Values{}), []string{"OwnerName", "RootProfile", "SubagentMaxDepth", "SessionCheckInInterval", "ShellSilenceMinutes"}, []string{"RulesMaxChars", "TestSlots", "PRTrustedBots", "PROutsideComments", "LocalLaneRepos", "DoctorCondition", "LibraryEnabled", "LibraryProfile", "LibrarySmellProfile", "LibraryReadmeMaxLines", "LibrarySmellsPerDay", "LocalCIRemote", "BrowserPath", "BrowserExtraFlags", "BrowserUseFlagsFile", "BrowserProfileMode", "BrowserOwnerProfile", "BrowserSaveBack", "ComputersEnabled", "ComputersReminderMinutes", "ComputersRenderNode", "MaxCardBytes", "MaxFileBytes", "AutoCheck", "DatabaseCallerDetail", "RunsRetention", "WorktreesRetention", "WorktreesRoot", "WorktreesMinFreeBytes", "SteeringMaxToolWait", "HarnessIdleUnloadMinutes", "PiCodeMode", "VoiceProvider", "VoiceModel", "VoiceURL", "VoiceDucking", "VoiceAgent", "OwnerAvatar", "Extra"}},
		{"Room", reflect.TypeOf(room.Record{}), []string{"ID", "Name", "Repo", "ExtraRepos", "Color", "Limits", "RootProfile", "SessionPause", "Upstream", "RepoUpstreams"}, []string{"MergeMaxParallel", "GitHubAccess", "ApprovalWait", "ApprovalExpiry", "LibraryPinSet", "LibraryPin", "CheckIn", "DisplayName", "ManualArchive", "Pinned", "PinOrder", "CreatedBy", "CreatedAt", "LastActivity"}},
		{"Session", reflect.TypeOf(subagent.Meta{}), []string{"ID", "Name", "RoomID", "Room", "Profile", "ParentSession"}, []string{"Status", "Closed", "Turns", "HistoryOnly", "Service", "Leaf", "Run", "Role", "InheritedReceipts", "UncertainInputIDs", "ConsolidationHistory", "ClosedPosts", "RoomCursor", "OwnerDMCursor", "Model", "Transfer", "Consolidation", "Consolidating", "ConsumedBy", "ConsolidationCharter", "InheritedInboxes", "FallbackPath", "Dir", "Depth", "Goal", "Item", "SpawnBrief", "Owner", "Created", "LastActivity", "Finished", "WrapUpAfter", "Closing", "HarnessSession", "RuntimePin", "Instructions", "InstructionsStale", "InstructionsKnown", "InstructionsNotes", "InstructionsSeq", "InstructionsChangedAt", "OwnerDisplay", "Resumable", "Usage", "UsageBaseline", "UsageBaselineTurn", "ContextTokens", "Pid"}},
	}
	// A handoff names fields that need review, not their potentially sensitive values.
	handoffs := map[string][]string{
		"Hook": {"Name", "Label", "Event", "Command", "Cwd", "Timeout", "OnFail", "Scope", "AddedBy", "AddedAt", "EditedBy", "EditedAt", "EveryMinutes"},
		"Room": {"Attribution", "AttributionPlacement", "AttributionKinds"},
	}
	out := make([]fieldInventory, 0, len(specs))
	for _, spec := range specs {
		item := fieldInventory{Type: spec.name}
		for i := 0; i < spec.typ.NumField(); i++ {
			field := spec.typ.Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			switch {
			case slices.Contains(spec.carried, field.Name):
				item.Carried = append(item.Carried, field.Name)
			case slices.Contains(spec.dropped, field.Name):
				item.Dropped = append(item.Dropped, field.Name)
			case slices.Contains(handoffs[spec.name], field.Name):
				item.Handoff = append(item.Handoff, field.Name)
			default:
				item.Gap = append(item.Gap, field.Name)
			}
		}
		out = append(out, item)
	}
	return out
}
