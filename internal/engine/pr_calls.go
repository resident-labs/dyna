package engine

import "dyna-agent/internal/websvc"

// prCalls are the PR mutations that the web app and the CLI share. Each acts
// for its caller (requirements item 3): pr/view passes authority.Of(ctx) to
// the PR service, which runs the same authority check as the CLI command.
var prCalls = []string{"DismissPRComment", "AdmitPRComment", "CommentPR", "PostPRReview", "LearnPRNoise", "TrackPR", "TrackPRRef", "TransferPR", "SetPRDraft", "SetPRGoal", "RequirePRReviewers", "UntrackPR", "ResumePR", "SetRoomChangePolicy", "SetChangePolicyOverride", "ReplyToFinding", "SetReviewEnabled", "SetRoomCIBudget", "SendDiffComments", "SendWorktreeComments", "RestartReviewer", "SetReviewerProfile", "SetPRSettings", "RemovePRNoiseRule", "SetMergeSettings", "SetReviewPublishing", "CloseChange", "OpenChange", "RecordPRVerdict", "MergeFixPR", "MergeControl", "SetGitHubAccess"}

func protectPRCalls(calls map[string]websvc.Call) {
	protectCalls(calls, prCalls, "changing PR state", nil)
}
