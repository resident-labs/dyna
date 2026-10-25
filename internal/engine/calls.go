package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"dyna-agent/internal/account/service"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/computerweb"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/devicesvc"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runsvc"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/voice"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/worktreesvc"
)

type services struct {
	devices   *devicesvc.Service
	files     *filesvc.Service
	git       *gitsvc.Service
	rooms     *roomsvc.Service
	terminals *terminalsvc.Service
	runtime   *harnessruntime.Runtime
	accounts  *service.Service
	config    *configsvc.Service
	computers *computerweb.Host
	voice     *voice.Calls
	hooks     hooks.Service
	sync      *lanesync.Service
	health    func() Health
	load      *sysload.Sampler
}

// SeatBindings are the root seat calls of dyna root and the web app. Tests
// that serve them build them here too.
func SeatBindings(rooms *roomsvc.Service) []websvc.Binding {
	return []websvc.Binding{
		{Name: "ReturnSeat", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ReturnSeat},
		// Only dyna root attach takes the seat; no UI does.
		{Name: "AttachSeat", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.AttachSeat},
	}
}

// SessionBindings are the session lifecycle calls that the CLI and the web
// app share, over rooms. The engine's table includes them, and tests that
// serve them build them here, so the two cannot drift apart.
func SessionBindings(rooms *roomsvc.Service) []websvc.Binding {
	return []websvc.Binding{
		{Name: "StopSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.StopSession},
		{Name: "CloseSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.CloseSession},
		{Name: "ForceCloseSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ForceCloseSession},
		// Only the CLI closes with choices; no UI does, so this call stays on
		// the engine's own socket.
		{Name: "CloseSessionOptions", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.CloseSessionOptions},
		{Name: "ReopenSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ReopenSession},
		{Name: "PromoteSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.PromoteSession},
		// Only the CLI starts a subagent, adopts and renames one session; no UI
		// does, so these calls stay on the engine's own socket.
		{Name: "StartSubagent", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.StartSubagent},
		{Name: "AdoptSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.AdoptSession},
		{Name: "RenameSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.RenameSession},
		{Name: "DismissSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.DismissSession},
		{Name: "DismissPendingPost", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.DismissPendingPost},
		{Name: "PauseRoomSessions", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.PauseRoomSessions},
		{Name: "ResumeRoomSessions", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ResumeRoomSessions},
		{Name: "ClearDormantSessions", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ClearDormantSessions},
		{Name: "TransferSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.TransferSession},
		{Name: "CancelSessionTransfer", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.CancelSessionTransfer},
		// Only the CLI hands off; no UI does, so the call stays on the engine's
		// own socket.
		{Name: "SubmitTransferHandoff", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: rooms.SubmitTransferHandoff},
		{Name: "ConsolidateSessions", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ConsolidateSessions},
		{Name: "ResumeSessionConsolidation", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.ResumeSessionConsolidation},
		{Name: "CancelSessionConsolidation", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.CancelSessionConsolidation},
	}
}

// SessionCalls binds SessionBindings and applies the engine's caller policy
// to them, as the engine does.
func SessionCalls(rooms *roomsvc.Service) (map[string]websvc.Call, error) {
	calls, err := websvc.Bind(SessionBindings(rooms))
	if err != nil {
		return nil, err
	}
	protectSessionCalls(calls)
	return calls, nil
}

func bindings(s services) []websvc.Binding {
	lead := append(append(SessionBindings(s.rooms), SeatBindings(s.rooms)...), RunnerBindings(func() *subagent.Manager { return s.rooms.Notify.Sessions })...)
	lead = append(lead, RunLifecycleBindings()...)
	machine := s.machineAPI()
	return append(lead, []websvc.Binding{
		s.admissionBinding(),
		s.claudeUsageBinding(),
		s.webSearchBinding(),
		{Name: "EnableLibrarian", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.enableLibrarian},
		{Name: "DisableLibrarian", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.disableLibrarian},
		{Name: "WakeLibrarian", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.wakeLibrarian},
		{Name: "FileLibrarySmell", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.fileLibrarySmell},
		{Name: "AddLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.addLibraryEntry},
		{Name: "UpdateLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.updateLibraryEntry},
		{Name: "LinkLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.linkLibraryEntry},
		{Name: "RetireLibraryEntry", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.retireLibraryEntry},
		{Name: "NewWorktree", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.newWorktree, Local: true},
		{Name: "GCWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.GC, Local: true},
		{Name: "RemoveWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.Remove, Local: true},
		{Name: "PruneWorktrees", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.Prune, Local: true},
		{Name: "ApplyRunWorktreeDisposition", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: worktreesvc.ApplyRunDisposition, Local: true},
		{Name: "Health", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.health},
		{Name: "SystemLoad", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.systemLoad},
		{Name: "Machines", Access: websvc.Read, Scope: websvc.ScopeRead, Method: machine.Machines},
		{Name: "Machine", Access: websvc.Read, Scope: websvc.ScopeRead, Method: machine.Machine},
		// Changing a machine's roles or route retargets the engine's work, so
		// the write needs the admin scope, like other settings.
		{Name: "SaveMachine", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: machine.SaveMachine},
		// A probe reads the machine now and stores what it found. It writes
		// only the probe fields, and it spends one SSH connection, so it needs
		// the admin scope too.
		{Name: "ProbeMachine", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: machine.ProbeMachine},
		{Name: "Rooms", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Rooms},
		{Name: "FeedTail", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.FeedTail},
		{Name: "FeedAfter", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.FeedAfter},
		{Name: "FeedAround", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.FeedAround},
		{Name: "SearchFeed", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.SearchFeed},
		{Name: "OpenRoom", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Open},
		{Name: "Members", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Members},
		{Name: "MentionChoices", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.MentionChoices},
		{Name: "Preview", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Preview},
		{Name: "MarkRead", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.MarkRead},
		{Name: "MarkReadMany", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.MarkReadMany},
		{Name: "Transcript", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Transcript},
		{Name: "TranscriptMore", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.TranscriptMore},
		{Name: "RootSession", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.RootSession},
		{Name: "TranscriptPage", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.TranscriptPage},
		{Name: "TranscriptAfter", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.TranscriptAfter},
		{Name: "TranscriptTurn", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.TranscriptTurn},
		{Name: "Profile", Access: websvc.Read, Scope: websvc.ScopeRead, Method: func(ref string) (roomsvc.Profile, error) { return s.rooms.Profile(ref, 3) }},
		{Name: "SendToSession", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, ref, text string, atts []room.Attachment, forwardOf string) (roomsvc.PostResult, error) {
			return s.rooms.SendToSession(ctx, ref, text, forwardOf, atts...)
		}},
		{Name: "Post", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.post},
		{Name: "Send", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, roomID, draft, replyTo string, atts []room.Attachment, forwardOf, conversation string) (roomsvc.SendResult, error) {
			return s.rooms.SendFromConversation(ctx, roomID, draft, replyTo, forwardOf, room.Conversation(conversation), nil, atts...)
		}},
		{Name: "SendWithReferences", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, roomID, draft, replyTo string, atts []room.Attachment, forwardOf string, references []room.Reference, conversation string) (roomsvc.SendResult, error) {
			return s.rooms.SendFromConversation(ctx, roomID, draft, replyTo, forwardOf, room.Conversation(conversation), references, atts...)
		}},
		{Name: "SendToSessionWithReferences", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, ref, text string, atts []room.Attachment, forwardOf string, references []room.Reference) (roomsvc.PostResult, error) {
			return s.rooms.SendToSessionWithReferences(ctx, ref, text, forwardOf, references, atts...)
		}},
		{Name: "Profiles", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Profiles},
		{Name: "BulkSessionAction", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.BulkSessionAction},
		{Name: "RoomSessionControls", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.RoomSessionControls},
		{Name: "SessionTransfer", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.SessionTransfer},
		{Name: "SessionConsolidations", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.SessionConsolidations},
		{Name: "RoomCIBudget", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.RoomCIBudget},
		{Name: "CIHealth", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.CIHealth},
		{Name: "CIHealthRun", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.CIHealthRun},
		{Name: "CIHealthTest", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.CIHealthTest},
		{Name: "SetRoomCIBudget", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetRoomCIBudget},
		{Name: "RoomChangePolicy", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.RoomChangePolicy},
		{Name: "SetRoomChangePolicy", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetRoomChangePolicy},
		{Name: "SetChangePolicyOverride", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetChangePolicyOverride},
		{Name: "Changes", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.Changes},
		{Name: "ChangeDetail", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.ChangeDetail},
		{Name: "MergeStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.MergeStatus},
		{Name: "MergeControlStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.MergeControlStatus},
		{Name: "SetMergePaused", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetMergePaused},
		{Name: "ResumeMerging", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, roomRef string) error {
			return s.rooms.PRs.MergeControl(ctx, roomRef, "resume", nil, nil, nil)
		}},
		{Name: "RoomMergeStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.RoomMergeStatus},
		{Name: "RoomMergeHistory", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.RoomMergeHistory},
		{Name: "ReplyToFinding", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.ReplyToFinding},
		{Name: "SendDiffComments", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.SendDiffComments},
		{Name: "SendWorktreeComments", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.SendWorktreeComments},
		{Name: "ReviewerStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.ReviewerStatus},
		{Name: "SetReviewerProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetReviewerProfile},
		{Name: "SetReviewEnabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetReviewEnabled},
		{Name: "SetMergeSettings", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetMergeSettings},
		{Name: "CloseChange", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.CloseChange},
		{Name: "RecordReviewResult", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.RecordReviewResult},
		// RecordPRVerdict is the name that older dyna builds still send; its
		// shim records through RecordReviewResult. See callspec.LegacyCalls.
		{Name: "RecordPRVerdict", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.RecordPRVerdict},
		{Name: "OpenChange", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.rooms.PRs.OpenChange},
		{Name: "MergeControl", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.rooms.PRs.MergeControl},
		{Name: "MergeFixPR", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.rooms.PRs.MergeFixPR},
		{Name: "SetGitHubAccess", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetGitHubAccess},
		{Name: "SetReviewPublishing", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetReviewPublishing},
		{Name: "ListServices", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.ListServices},
		{Name: "SetServiceProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.SetServiceProfile},
		{Name: "RestartReviewer", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.RestartReviewer},
		{Name: "ListHooks", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.List},
		{Name: "ListGlobalHooks", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.ListGlobal},
		{Name: "HookSummary", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.Summaries},
		{Name: "GlobalHookSummary", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.SummariesGlobal},
		{Name: "HookRuns", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.Runs},
		{Name: "GlobalHookRuns", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name string) ([]hooks.Run, error) {
			return s.hooks.Runs(ctx, "", name)
		}},
		{Name: "RunHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string) (hooks.Run, error) {
			by, err := actor(ctx)
			if err != nil {
				return hooks.Run{}, err
			}
			return s.hooks.Run(ctx, hooks.Event{Repo: repo, Session: by, Room: authority.Of(ctx).Room}, name)
		}},
		{Name: "TestHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string) (hooks.Run, error) {
			by, err := actor(ctx)
			if err != nil {
				return hooks.Run{}, err
			}
			return s.hooks.Run(ctx, hooks.Event{Repo: repo, Session: by, Room: authority.Of(ctx).Room, Branch: "sample-branch", SHA: "0000000000000000000000000000000000000000", PR: "1"}, name)
		}},
		{Name: "RunGlobalHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name string) (hooks.Run, error) {
			by, err := actor(ctx)
			if err != nil {
				return hooks.Run{}, err
			}
			return s.hooks.RunGlobal(ctx, hooks.Event{Session: by}, name)
		}},
		{Name: "ReconcileHookInstall", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.hooks.ReconcileInstall},
		{Name: "AddHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.addHook},
		{Name: "EditHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, repo, name string, patch hooks.Patch) error {
			by, err := actor(ctx)
			if err != nil {
				return err
			}
			return s.hooks.Edit(ctx, repo, name, by, patch)
		}},
		{Name: "RemoveHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.hooks.Remove},
		{Name: "AddGlobalHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.addGlobalHook},
		{Name: "EditGlobalHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name string, patch hooks.Patch) error {
			by, err := actor(ctx)
			if err != nil {
				return err
			}
			return s.hooks.Edit(ctx, "", name, by, patch)
		}},
		{Name: "RemoveGlobalHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name string) error {
			return s.hooks.Remove(ctx, "", name)
		}},
		{Name: "ResetHook", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name string) error {
			by, err := actor(ctx)
			if err != nil {
				return err
			}
			return s.hooks.Reset(ctx, name, by)
		}},
		{Name: "HookRunLog", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.hooks.HookRunLog},
		{Name: "GlobalHookRunLog", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: func(ctx context.Context, name, id string) (hooks.HookRunLog, error) {
			return s.hooks.HookRunLog("", name, id)
		}},
		{Name: "PRSettings", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.PRSettings},
		{Name: "CommentPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.CommentPR},
		{Name: "PostPRReview", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.PostPRReview},
		{Name: "HeldPRComments", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.HeldPRComments},
		{Name: "DismissPRComment", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.DismissPRComment},
		{Name: "AdmitPRComment", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.AdmitPRComment},
		{Name: "LearnPRNoise", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.LearnPRNoise},
		{Name: "PRNoiseRules", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.PRNoiseRules},
		{Name: "RemovePRNoiseRule", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.RemovePRNoiseRule},
		{Name: "SetPRSettings", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PRs.SetPRSettings},
		{Name: "UntrackPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.UntrackPR},
		{Name: "ResumePR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.ResumePR},
		{Name: "TrackPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.TrackPR},
		// The CLI's track names a checkout path, so no paired device sends it.
		{Name: "TrackPRRef", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.rooms.PRs.TrackPRRef},
		{Name: "TransferPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.rooms.PRs.TransferPR},
		{Name: "SetPRDraft", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.SetPRDraft},
		{Name: "RequirePRReviewers", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.RequirePRReviewers},
		{Name: "GitHubList", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.GitHubList},
		{Name: "GitHubView", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.GitHubView},
		{Name: "StartIssueWork", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.StartIssueWork},
		{Name: "GitHubDiff", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.PRs.GitHubDiff},
		{Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.stampPlanAncestry, Local: true},
		{Name: "Runs", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Runs},
		{Name: "RunDetail", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.RunDetail},
		{Name: "RunResult", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.RunResult},
		{Name: "Attach", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.AttachBase64},
		{Name: "AttachmentText", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.AttachmentText},
		{Name: "Pin", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.pin},
		{Name: "Unpin", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.Unpin},
		{Name: "Pins", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Pins},
		{Name: "Issues", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Issues},
		{Name: "Library", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Library},
		{Name: "CloseIssue", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.CloseIssue},
		{Name: "IssueGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.IssueGoal},
		{Name: "ExportIssue", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.ExportIssue},
		{Name: "RequestCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.requestCall, Local: true},
		{Name: "WithdrawCall", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.withdrawCall, Local: true},
		{Name: "CallRequests", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.CallRequests},
		{Name: "CallRequestShown", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.CallRequestShown},
		{Name: "DeclineCallRequest", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.DeclineCallRequest},
		{Name: "Questions", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Questions},
		{Name: "Visuals", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Visuals},
		{Name: "AskQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.askQuestion},
		{Name: "WithdrawQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.withdrawQuestion, Local: true},
		{Name: "AnswerQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.answerQuestion},
		{Name: "Rules", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Rules},
		{Name: "AddRule", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.AddRule},
		{Name: "EditRule", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.EditRule},
		{Name: "RemoveRule", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.RemoveRule},
		{Name: "Goals", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Goals},
		{Name: "Goal", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Goal},
		{Name: "GoalJournal", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.GoalJournal},
		{Name: "GoalItems", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.GoalItems},
		{Name: "GoalStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.GoalStatus},
		{Name: "GoalEvents", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.GoalEvents},
		{Name: "GoalItemCounts", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.GoalItemCounts},
		{Name: "AddGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.AddGoal, Local: true},
		{Name: "AddGoalItem", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.AddGoalItem, Local: true},
		{Name: "MoveGoalItem", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.MoveGoalItem, Local: true},
		{Name: "EditGoalSelectors", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.EditGoalSelectors, Local: true},
		{Name: "EditGoalItem", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.EditGoalItem, Local: true},
		{Name: "EditGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.EditGoal},
		{Name: "FinishGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.FinishGoal},
		{Name: "ExpediteGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.ExpediteGoal},
		{Name: "AmendGoalBrief", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.AmendGoalBrief, Local: true},
		{Name: "SetPRGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.PRs.SetPRGoal},
		{Name: "SetGoalItemState", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.SetGoalItemState},
		{Name: "NoteGoal", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.NoteGoal},
		{Name: "ListEntries", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.files.ListEntries},
		{Name: "ReadFile", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.files.ReadFile},
		{Name: "SyncStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.sync.Status},
		{Name: "SyncFetch", Access: websvc.Write, Scope: websvc.ScopeFiles, Method: s.syncFetch, Local: true},
		{Name: "SyncRefresh", Access: websvc.Write, Scope: websvc.ScopeFiles, Method: s.syncRefresh},
		{Name: "GitExport", Access: websvc.Write, Scope: websvc.ScopeFiles, Method: s.gitExport},
		{Name: "GitStatus", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitStatus},
		{Name: "GitLastCommits", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitLastCommits},
		{Name: "GitReadFile", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitReadFile},
		{Name: "GitHistory", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitHistory},
		{Name: "GitBlame", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitBlame},
		{Name: "GitDiff", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.git.GitDiff},
		{Name: "TemplateSeed", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.templateSeed, Local: true},
		{Name: "ListTemplates", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.ListTemplates},
		{Name: "StartRun", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.StartRun},
		{Name: "CancelRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.CancelRun},
		{Name: "PauseRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: runsvc.Pause},
		{Name: "RemoveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Remove},
		{Name: "ClearRuns", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Clear},
		{Name: "ArchiveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Archive},
		{Name: "UnarchiveRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: runsvc.Unarchive},
		{Name: "SteerRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.SteerRun},
		{Name: "Devices", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.devices.Devices},
		{Name: "RevokeDevice", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.devices.RevokeDevice},
		{Name: "SetDeviceScopes", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.devices.SetScopes},
		{Name: "CreatePairing", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.devices.CreatePairing},
		{Name: "SessionQueue", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.SessionQueue},
		{Name: "DeletePendingSteer", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.DeletePendingSteer},
		{Name: "ArchiveRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.ArchiveRoom},
		{Name: "UnarchiveRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.UnarchiveRoom},
		{Name: "RenameRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.RenameRoom},
		{Name: "SetRoomRootProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.SetRoomRootProfile},
		{Name: "DeleteRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.DeleteRoom},
		{Name: "PinRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.PinRoom},
		{Name: "UnpinRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.UnpinRoom},
		{Name: "TermOpen", Access: websvc.Write, Scope: websvc.ScopeTerminal, Method: s.terminals.TermOpen},
		{Name: "Terms", Access: websvc.Read, Scope: websvc.ScopeTerminal, Method: func() []terminalsvc.Info { return s.terminals.List("") }},
		{Name: "TermAttach", Access: websvc.Read, Scope: websvc.ScopeTerminal, Method: s.terminals.TermAttach},
		{Name: "TermWrite", Access: websvc.Write, Scope: websvc.ScopeTerminal, Method: s.terminals.TermWrite},
		{Name: "TermResize", Access: websvc.Write, Scope: websvc.ScopeTerminal, Method: s.terminals.Resize},
		{Name: "TermClose", Access: websvc.Write, Scope: websvc.ScopeTerminal, Method: s.terminals.Close},
		{Name: "ShellJobs", Access: websvc.Read, Scope: websvc.ScopeTerminal, Method: s.terminals.ShellJobs},
		{Name: "ShellLog", Access: websvc.Read, Scope: websvc.ScopeTerminal, Method: s.terminals.ShellLog},
		{Name: "ShellLogRange", Access: websvc.Read, Scope: websvc.ScopeTerminal, Method: s.terminals.ShellLogRange},
		{Name: "StopShell", Access: websvc.Write, Scope: websvc.ScopeTerminal, Method: s.terminals.StopShell},
		{Name: "ConfigCheckProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.ConfigCheckProfile},
		{Name: "ConfigProfiles", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: configsvc.Profiles},
		{Name: "UpsertProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.config.UpsertProfile},
		{Name: "SetProfileUses", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.config.SetProfileUses},
		{Name: "SaveProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.SaveProfile},
		{Name: "RemoveProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.RemoveProfile},
		{Name: "SetDefaultProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.SetDefaultProfile},
		{Name: "SetProfileDisabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.SetProfileDisabled},
		{Name: "Settings", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: configsvc.Items},
		{Name: "SetSetting", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.setSetting},
		{Name: "UpdateNotice", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.config.UpdateNotice},
		{Name: "Notices", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.Notices},
		{Name: "DismissNotice", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.rooms.DismissNotice},
		{Name: "UpdateDyna", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.rooms.UpdateDyna},
		{Name: "DataDir", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: store.DataDir},
		{Name: "BrowseInfo", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.files.BrowseInfo},
		{Name: "BrowseDirs", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.files.BrowseDirs},
		{Name: "InspectPath", Access: websvc.Read, Scope: websvc.ScopeFiles, Method: s.rooms.InspectPath},
		{Name: "CreateRoom", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.createRoom},
		{Name: "CreateRoomRecord", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.createRoomRecord},
		{Name: "RestartPrivateRuntimeLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.RestartPrivateRuntimeLogin},
		{Name: "PreviewPrivateConfig", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.PreviewPrivateConfig},
		{Name: "ImportPrivateConfig", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.ImportPrivateConfig},
		{Name: "PrivateRuntimes", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.PrivateRuntimes},
		{Name: "RefreshPrivateRuntimes", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.RefreshPrivateRuntimes},
		{Name: "RemovePrivateRuntime", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.RemovePrivateRuntime},
		{Name: "UninstallPrivateRuntime", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.UninstallPrivateRuntime},
		{Name: "InstallPrivateRuntime", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.InstallPrivateRuntime},
		{Name: "BeginPrivateRuntimeLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.BeginPrivateRuntimeLogin},
		{Name: "FindPersonalLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.PreparePersonalLogin},
		{Name: "UsePersonalLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.UsePersonalLoginSnapshot},
		{Name: "PrivateRuntimeLogins", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.PrivateRuntimeLogins},
		{Name: "PrivateRuntimeLoginState", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.PrivateRuntimeLoginState},
		{Name: "RespondPrivateRuntimeLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.RespondPrivateRuntimeLogin},
		{Name: "CancelPrivateRuntimeLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.CancelPrivateRuntimeLogin},
		{Name: "FinishPrivateRuntimeLogin", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.FinishPrivateRuntimeLogin},
		{Name: "SetAccountRepos", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.SetAccountRepos},
		{Name: "SetHarnessConfigEnabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.SetHarnessConfigEnabled},
		{Name: "SyncCatalogAccount", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.runtime.SyncCatalogAccount},
		{Name: "SyncPrivateRuntime", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.SyncPrivateRuntime},
		{Name: "GCPrivateRuntime", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.GCPrivateRuntime},
		{Name: "AddCatalogMCP", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.runtime.AddCatalogMCP},
		{Name: "AddCatalogSkill", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.runtime.AddCatalogSkill},
		{Name: "RemoveCatalogEntry", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.runtime.RemoveCatalogEntry},
		{Name: "SetCatalogEntryEnabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: s.runtime.SetCatalogEntryEnabled},
		{Name: "AddHarnessMCP", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.AddHarnessMCP},
		{Name: "AddHarnessSkill", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.AddHarnessSkill},
		{Name: "RemoveHarnessConfig", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.runtime.RemoveHarnessConfig},
		{Name: "HarnessConfig", Access: websvc.Read, Scope: websvc.ScopeAdmin, Method: s.runtime.HarnessConfig},
		// AccountsList is a write call because refresh asks each provider for
		// usage and saves the answer. AccountsView reads the saved view only, so
		// a read-only page (or a read-only QA pass) can still show the accounts.
		// It keeps the read scope of the retired AccountUsageCached call, because
		// the summary is the same data a paired read-only client already read.
		{Name: "AccountsList", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accountsList},
		{Name: "AccountsView", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.accountsView},
		{Name: "AccountsUse", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accountsUse},
		{Name: "AccountsRemove", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accountsRemove},
		{Name: "AccountsAdd", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accountsAdd},
		// AccountsImport moves a saved native sign-in into its Pi provider
		// account, so no two copies renew one refresh token.
		{Name: "AccountsImport", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accountsImport},
		{Name: "ProfileModels", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: s.accounts.ProfileModels},
		{Name: "UsageRollup", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.UsageRollup},
		{Name: "ProfileUsage", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.rooms.ProfileUsage},
		{Name: "Computers", Access: websvc.Read, Scope: websvc.ScopeComputers, Method: s.computers.Computers},
		{Name: "Computer", Access: websvc.Read, Scope: websvc.ScopeComputers, Method: s.computers.Computer},
		{Name: "ComputerControl", Access: websvc.Write, Scope: websvc.ScopeComputers, Method: s.computers.Control},
		{Name: "VoiceResume", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.voice.Resume},
		{Name: "VoiceStart", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.voice.Start},
		{Name: "VoiceStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.voice.Status},
		{Name: "VoiceStop", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: s.voice.Stop},
	}...)
}

func callsFor(s services, readOnly bool) (map[string]websvc.Call, map[string]websvc.Scope, error) {
	table := bindings(s)
	calls, err := websvc.Bind(table)
	if err != nil {
		return nil, nil, err
	}
	protectGoalCalls(calls)
	protectSessionCalls(calls)
	protectPRCalls(calls)
	protectIssueCalls(calls)
	protectMachineCalls(calls)
	protectOwnerCalls(calls)
	protectCLICalls(calls)
	if readOnly {
		restrictWrites(calls, table)
	}
	scopes := make(map[string]websvc.Scope, len(table))
	for _, binding := range table {
		scopes[binding.Name] = binding.Scope
	}
	return calls, scopes, nil
}

// ReadOnlyWebMethods returns the shared call table's read methods without starting services.
// QA uses this table to fail closed when a method is added or changes access.
func ReadOnlyWebMethods() []string {
	var names []string
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		if binding.Access == websvc.Read {
			names = append(names, binding.Name)
		}
	}
	return names
}

func restrictWrites(calls map[string]websvc.Call, table []websvc.Binding) {
	for _, binding := range table {
		if binding.Access == websvc.Write {
			calls[binding.Name] = func(context.Context, []json.RawMessage) (any, error) { return nil, websvc.ErrReadOnly }
		}
	}
}

func actor(ctx context.Context) (string, error) {
	caller, local := websvc.CallerFromContext(ctx)
	as, ok, err := recordedCaller(caller, local, authority.Of(ctx), enforceCallers, log.Printf, "changing hooks")
	switch {
	case err != nil:
		return "", err
	case !ok:
		return unverified, nil
	case as.Run != "":
		return workerName(as), nil
	case as.Session != "":
		return as.Session, nil
	}
	if p, ok := websvc.PrincipalFromContext(ctx); ok && p.Kind != "local" && p.Name != "" {
		return p.Name, nil
	}
	return "owner", nil
}

// terminalOptions supplies store access without a terminalsvc import of the session runtime.
func terminalOptions(rooms *roomsvc.Service, idle time.Duration) terminalsvc.Options {
	return terminalsvc.Options{
		IdleTimeout: idle,
		Rooms:       rooms.Notify.Rooms,
		SessionRoot: rooms.Notify.Sessions.Store.Root,
		SessionDirectory: func(ref string) (string, string, string, error) {
			meta, err := rooms.Notify.Sessions.Store.ReadMeta(ref)
			return meta.RoomID, meta.ID, meta.Dir, err
		},
	}
}

// SystemLoad is the machine load with each top row named.
type SystemLoad struct {
	sysload.Load
	Top []roomsvc.LoadOwnerCores `json:"top"`
}

func (s services) systemLoad() SystemLoad {
	l := s.load.Load()
	out := SystemLoad{Load: l, Top: []roomsvc.LoadOwnerCores{}}
	for _, t := range l.Top {
		out.Top = append(out.Top, roomsvc.LoadOwnerCores{LoadOwner: s.rooms.LoadOwner(t.ID), Cores: t.Cores})
	}
	return out
}

// accountsList returns the Accounts view grouped by provider. scope is a room
// reference, whose row overrides the global one, or empty for the global
// selection alone.
func (s services) accountsList(ctx context.Context, scope string, refresh bool) (service.AccountsSnapshot, error) {
	roomID, err := s.accountScope(scope)
	if err != nil {
		return service.AccountsSnapshot{}, err
	}
	return s.accounts.Accounts(ctx, roomID, refresh), nil
}

// accountsView returns the Accounts view of its saved readings, without asking a
// provider for new usage: a read that changes nothing.
func (s services) accountsView(ctx context.Context, scope string) (service.AccountsSnapshot, error) {
	roomID, err := s.accountScope(scope)
	if err != nil {
		return service.AccountsSnapshot{}, err
	}
	return s.accounts.Accounts(ctx, roomID, false), nil
}

// accountsUse makes an account the one its provider uses in scope: that room
// when scope names one, else globally.
func (s services) accountsUse(ctx context.Context, id, scope string) (service.AccountsSnapshot, error) {
	roomID, err := s.accountScope(scope)
	if err != nil {
		return service.AccountsSnapshot{}, err
	}
	if roomID != "" {
		// A room choice changes that room's future turns, so a session may choose
		// only for its own room. The owner may choose for any room.
		if _, err := authority.ActorIn(ctx, roomID); err != nil {
			return service.AccountsSnapshot{}, err
		}
	}
	if err := s.runtime.UseAccount(ctx, id, roomID); err != nil {
		return service.AccountsSnapshot{}, err
	}
	return s.accounts.Accounts(ctx, roomID, false), nil
}

// accountsRemove deletes an account and its homes. The caller asked for the
// removal, so the engine needs no separate confirmation argument.
func (s services) accountsRemove(ctx context.Context, id string) (service.AccountsSnapshot, error) {
	if err := s.runtime.RemoveAccount(id); err != nil {
		return service.AccountsSnapshot{}, err
	}
	return s.accounts.Accounts(ctx, "", false), nil
}

// accountScope reads a room reference as the scope key of an account selection.
// An empty reference is the global scope.
func (s services) accountScope(scope string) (string, error) {
	if strings.TrimSpace(scope) == "" {
		return "", nil
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(scope)
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// accountsAdd adds an API key as a new account of one provider for harness:
// empty or "pi" keeps it in a Pi account. The CLI reads the key from standard
// input and sends it once; it is never a CLI argument.
func (s services) accountsAdd(ctx context.Context, harness, provider, label, key string) (service.AccountsSnapshot, error) {
	p, ok := harnessruntime.PiProvider(provider)
	if !ok {
		return service.AccountsSnapshot{}, errors.New("Unknown provider. Choose a provider Dyna knows, such as anthropic, deepseek or zai.")
	}
	if key == "" {
		return service.AccountsSnapshot{}, errors.New("An API key is required on standard input.")
	}
	if _, err := s.runtime.AddProviderKey(ctx, harness, p, label, key); err != nil {
		return service.AccountsSnapshot{}, err
	}
	return s.accounts.Accounts(ctx, "", false), nil
}
