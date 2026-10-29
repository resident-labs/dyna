package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// mainCallerPolicies is the exact name-to-policy assignment that the seven
// caller-policy name lists on main had: goals and rules, sessions, CLI
// commands, PR state, machines, owner controls and room issues. It proves the
// move onto bindings lost and added nothing; shrink it toward per-policy spot
// checks as the table changes.
var mainCallerPolicies = map[websvc.Policy][]string{
	policyGoal: {
		"AddGoal",
		"AddGoalItem",
		"MoveGoalItem",
		"EditGoalSelectors",
		"EditGoalItem",
		"EditGoal",
		"AmendGoalBrief",
		"FinishGoal",
		"ExpediteGoal",
		"SetGoalItemState",
		"NoteGoal",
		"AddRule",
		"EditRule",
		"RemoveRule",
	},
	policySession: {
		"StopSession",
		"CloseSession",
		"ForceCloseSession",
		"CloseSessionOptions",
		"ReopenSession",
		"PromoteSession",
		"AdoptSession",
		"RenameSession",
		"StartSubagent",
		"DismissSession",
		"DismissPendingPost",
		"PauseRoomSessions",
		"ResumeRoomSessions",
		"ClearDormantSessions",
		"TransferSession",
		"CancelSessionTransfer",
		"SubmitTransferHandoff",
		"ConsolidateSessions",
		"ResumeSessionConsolidation",
		"CancelSessionConsolidation",
	},
	policyCLI: {
		"SetSetting",
		"SetRoomRootProfile",
		"SetRoomCheckIn",
		"SetRoomLimit",
		"AddRoomRepo",
		"RemoveRoomRepo",
		"SaveProfile",
		"UpsertProfile",
		"SetProfileUses",
		"RemoveProfile",
		"SetDefaultProfile",
		"SetProfileDisabled",
		"ImportPrivateConfig",
		"AddHarnessMCP",
		"AddCatalogMCP",
		"AddCatalogSkill",
		"RemoveCatalogEntry",
		"SetCatalogEntryEnabled",
		"AddHarnessSkill",
		"RemoveHarnessConfig",
		"SetHarnessConfigEnabled",
		"SyncPrivateRuntime",
		"SyncCatalogAccount",
		"InstallPrivateRuntime",
		"RemovePrivateRuntime",
		"RestartPrivateRuntimeLogin",
		"BeginPrivateRuntimeLogin",
		"FinishPrivateRuntimeLogin",
		"UsePersonalLogin",
		"FindPersonalLogin",
		"GCPrivateRuntime",
		"UpdateDyna",
		"ReconcileHookInstall",
		"RemoveHook",
		"ConfigCheckProfile",
		"GitExport",
		"StartRun",
		"AccountsList",
		"AccountsUse",
		"AccountsRemove",
		"AccountsImport",
		"ArchiveRun",
		"UnarchiveRun",
		"CancelRun",
		"PauseRun",
		"TransferRun",
		"RemoveRun",
		"ClearRuns",
		"SyncRefresh",
		"Unpin",
	},
	policyPR: {
		"DismissPRComment",
		"AdmitPRComment",
		"CommentPR",
		"EditPR",
		"PostPRReview",
		"LearnPRNoise",
		"TrackPR",
		"TrackPRRef",
		"TransferPR",
		"SetPRDraft",
		"ClosePR",
		"SetPRGoal",
		"RequirePRReviewers",
		"ReleasePRReviewers",
		"UntrackPR",
		"ResumePR",
		"SetRoomChangePolicy",
		"SetChangePolicyOverride",
		"ReplyToFinding",
		"SetReviewEnabled",
		"SetRoomCIBudget",
		"SendDiffComments",
		"SendWorktreeComments",
		"RestartReviewer",
		"SetReviewerProfile",
		"SetPRSettings",
		"RemovePRNoiseRule",
		"SetMergeSettings",
		"SetReviewPublishing",
		"CloseChange",
		"OpenChange",
		"RecordReviewResult",
		"RecordPRVerdict",
		"MergeFixPR",
		"MergeControl",
		"SetMergePaused",
		"ResumeMerging",
		"SetGitHubAccess",
	},
	policyMachine: {"SaveNode"},
	policyOwner: {
		"TermWrite",
		"TermOpen",
		"TermRequestRun",
		"TermRequestPreview",
		"TermRequestReport",
		"CreatePairing",
		"SetDeviceScopes",
		"RevokeDevice",
		"ComputerControl",
		"AccountsAdd",
		"SetAccountRepos",
		"UninstallPrivateRuntime",
		"CancelPrivateRuntimeLogin",
		"RespondPrivateRuntimeLogin",
		"StartIssueWork",
		"Send",
		"SendFromCard",
		"AnswerQuestionFromCard",
		"SendWithReferences",
		"SendToSessionWithReferences",
		"SendToSession",
		"DeclineCallRequest",
		"VoiceStart",
		"VoiceResume",
		"VoiceStop",
		"TermClose",
		"TermResize",
		"ProfileModels",
		"Attach",
		"ArchiveRoom",
		"UnarchiveRoom",
		"RenameRoom",
		"PinRoom",
		"UnpinRoom",
		"DeletePendingSteer",
		"CallRequestShown",
		"MarkRead",
		"MarkReadMany",
		"MarkRoomRead",
		"UpdateNotice",
		"DismissNotice",
		"ProbeNode",
	},
	policyIssue: {"CloseIssue", "IssueGoal", "ExportIssue"}}

// TestCallerPoliciesMatchMain checks that every binding with a caller policy
// carries the policy its name had on main, and that no name lost or gained
// one.
func TestCallerPoliciesMatchMain(t *testing.T) {
	want := map[string]websvc.Policy{}
	for policy, names := range mainCallerPolicies {
		for _, name := range names {
			if old, exists := want[name]; exists {
				t.Fatalf("%s has two main policies: %d and %d", name, old, policy)
			}
			want[name] = policy
		}
	}
	got := map[string]websvc.Policy{}
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		if binding.Policy == 0 {
			continue
		}
		main, ok := want[binding.Name]
		switch {
		case !ok:
			t.Errorf("%s carries caller policy %d, which main did not give it", binding.Name, binding.Policy)
		case main != binding.Policy:
			t.Errorf("%s carries caller policy %d; main had %d", binding.Name, binding.Policy, main)
		}
		got[binding.Name] = binding.Policy
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s lost the caller policy main gave it", name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d policy bindings; main had %d", len(got), len(want))
	}
}

// policyNames is every call in the engine's table that carries this caller
// policy.
func policyNames(policy websvc.Policy) []string {
	var names []string
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		if binding.Policy == policy {
			names = append(names, binding.Name)
		}
	}
	return names
}

// TestCallerPolicyWrapsTheCall proves a declared policy wraps its call: an
// unverified caller is refused with the policy's doing text before the bound
// method runs, while a binding without a policy runs exactly as bound.
func TestCallerPolicyWrapsTheCall(t *testing.T) {
	for policy, config := range callerPolicies {
		t.Run(config.doing, func(t *testing.T) {
			ran := false
			method := func(context.Context) error { ran = true; return nil }
			table := []websvc.Binding{
				{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: policy, Method: method},
				{Name: "Unguarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: method},
			}
			calls, err := websvc.Bind(table)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyCallerPolicies(calls, table); err != nil {
				t.Fatal(err)
			}
			refused := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
			if _, err := calls["Guarded"](refused, nil); err == nil || !strings.Contains(err.Error(), "could not verify who is "+config.doing) {
				t.Fatalf("unverified caller: %v", err)
			}
			if ran {
				t.Fatal("the guarded method ran for an unverified caller")
			}
			if _, err := calls["Unguarded"](refused, nil); err != nil {
				t.Fatalf("a binding without a policy was wrapped: %v", err)
			}
			if !ran {
				t.Fatal("the unguarded method did not run")
			}
		})
	}
}

// TestOwnerPolicyKeepsOwnerOnlyText checks the owner check keeps its exact
// refusal, so CLI output does not change.
func TestOwnerPolicyKeepsOwnerOnlyText(t *testing.T) {
	method := func(context.Context) error { return nil }
	table := []websvc.Binding{{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: policyOwner, Method: method}}
	calls, err := websvc.Bind(table)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCallerPolicies(calls, table); err != nil {
		t.Fatal(err)
	}
	session := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "ses_1"}, authority.Actor{Role: authority.Session, Session: "ses_1", Room: "room"})
	if _, err := calls["Guarded"](session, nil); err == nil || err.Error() != "Guarded is owner only" {
		t.Fatalf("session caller: %v", err)
	}
}

// TestUnknownCallerPolicyFails checks that a policy value the engine does not
// know fails before the server starts.
func TestUnknownCallerPolicyFails(t *testing.T) {
	table := []websvc.Binding{{Name: "Guarded", Access: websvc.Write, Scope: websvc.ScopeOperate, Policy: websvc.Policy(99), Method: func() error { return nil }}}
	calls, err := websvc.Bind(table)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCallerPolicies(calls, table); err == nil || !strings.Contains(err.Error(), "unknown caller policy") {
		t.Fatalf("unknown policy: %v", err)
	}
}
