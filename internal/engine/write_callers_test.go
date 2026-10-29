package engine

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/websvc"
)

// Lower this count whenever a fix removes an unchecked entry.
const maxUncheckedWrites = 0

// These handlers apply the caller policy themselves. Each reason names the
// check, so adding a name requires reviewing that implementation.
var handlerCallerChecks = map[string]string{
	"ObserveClaudeUsage":          "checkClaudeUsageCaller uses the verified execution caller and request prefix, then the service loads the complete immutable admission reference.",
	"AdmitNativeAttempt":          "checkAdmissionCaller requires the verified session runner or the run coordinator and its own execution and request prefix.",
	"AttachSeat":                  "Checks root attach with authority.Of(ctx).",
	"FinishRun":                   "checkRunCoordinator requires the run's verified coordinator and its own request prefix.",
	"StartRunAgent":               "checkRunAgentCoordinator requires the run's verified coordinator.",
	"CloseRunAgent":               "checkRunAgentCoordinator requires the run's verified coordinator.",
	"CloseRunAgents":              "checkRunAgentCoordinator requires the run's verified coordinator.",
	"AddRunDirectories":           "checkRunCoordinator requires the run's verified coordinator and its own request prefix.",
	"AddHook":                     "actor(ctx) resolves the author; hooks.Add checks git hooks add with authority.Of(ctx).",
	"AddGlobalHook":               "actor(ctx) resolves the author; hooks.AddGlobal checks git hooks add with authority.Of(ctx).",
	"AddLibraryEntry":             "libraryStore passes mutationActor to library authority checks.",
	"AnswerQuestion":              "postSender resolves the caller before answering the question.",
	"CancelJob":                   "Checks authority.Of(ctx): the owner, the submitting session or the job room's root.",
	"RunJob":                      "Records the submitting session and room from authority.Of(ctx); events wake only that session.",
	"ApplyRunWorktreeDisposition": "worktreesvc.caller checks authority.Of(ctx).",
	"AskQuestion":                 "questionSender resolves the caller before creating the question.",
	"BulkSessionAction":           "Each action passes ctx into the session manager authority guards.",
	"CreateRoom":                  "createdBy resolves the caller; roomsvc checks room create.",
	"CreateRoomRecord":            "createdBy resolves the caller; roomsvc checks room create.",
	"DeleteRoom":                  "Checks room delete with authority.Of(ctx).",
	"DisableLibrarian":            "librarianActor and librarian service disable rule.",
	"FileTerminalRequest":         "postSender resolves the caller before filing; notify refuses any caller that is not a session.",
	"ApproveRequest":              "questionSender resolves the caller; notify approves only for the session that holds the request.",
	"DenyRequest":                 "questionSender resolves the caller; notify denies only for the session that holds the request.",
	"WithdrawRequest":             "questionSender resolves the caller; notify withdraws a terminal request only for its filer.",
	"EditHook":                    "actor(ctx) resolves the editor; hooks.Edit checks git hooks edit with authority.Of(ctx).",
	"EditGlobalHook":              "actor(ctx) resolves the editor; hooks.Edit checks git hooks edit with authority.Of(ctx).",
	"EnableLibrarian":             "librarianActor and library enable rule.",
	"FileLibrarySmell":            "librarianActor resolves the caller and smell author.",
	"GCWorktrees":                 "worktreesvc.caller checks authority.Of(ctx).",
	"LinkLibraryEntry":            "libraryStore passes mutationActor to library authority checks.",
	"NewWorktree":                 "worktreeCreatedBy resolves the caller; worktreesvc.caller checks it.",
	"Pin":                         "pinnedBy resolves the caller before recording the pin.",
	"Post":                        "postSender resolves the caller before notify.Post.",
	"PruneWorktrees":              "worktreesvc.caller checks authority.Of(ctx).",
	"RemoveWorktrees":             "worktreesvc.caller checks authority.Of(ctx).",
	"RemoveGlobalHook":            "hooks.Remove checks git hooks rm with authority.Of(ctx).",
	"ResetHook":                   "actor(ctx) resolves the editor; hooks.Reset checks git hooks edit with authority.Of(ctx).",
	"RequestCall":                 "callSender resolves the caller; notify checks the stored root.",
	"RetireLibraryEntry":          "libraryStore passes mutationActor to library authority checks.",
	"ReturnSeat":                  "Checks root detach with authority.Of(ctx).",
	"RunHook":                     "actor(ctx) applies recordedCaller before running the hook.",
	"RunGlobalHook":               "actor(ctx) applies recordedCaller before running the hook.",
	"RunnerClaimInput":            "runnerSession requires the caller session and uses only that session ID.",
	"RunnerInstructions":          "runnerSession requires the caller session and uses only that session ID.",
	"RunnerReturnDormant":         "runnerSession requires the caller session and uses only that session ID.",
	"RunnerCompleteTurn":          "runnerSession requires the caller session and uses only that session ID.",
	"RunnerFinish":                "runnerSession requires the caller session and uses only that session ID.",
	"RunnerSetDMCursor":           "runnerSession requires the caller session and uses only that session ID.",
	"RunnerSetHarnessSession":     "runnerSession requires the caller session and uses only that session ID.",
	"RunnerSetInstructionsStale":  "runnerSession requires the caller session and uses only that session ID.",
	"RunnerSetPid":                "runnerSession requires the caller session and uses only that session ID.",
	"RunnerSetRuntimePin":         "runnerSession requires the caller session and uses only that session ID.",
	"RunnerTurnEnd":               "runnerSession requires the caller session and uses only that session ID.",
	"RunnerTurnNote":              "runnerSession requires the caller session and uses only that session ID.",
	"RunnerTurnStart":             "runnerSession requires the caller session and uses only that session ID.",
	"SetServiceProfile":           "Checks SetServiceProfile with authority.Of(ctx).",
	"SetServiceProfileAll":        "Checks SetServiceProfileAll with authority.Of(ctx).",
	"StampPlanAncestry":           "Checks authority.Of(ctx) and confines workers to their own run.",
	"StopShell":                   "Checks that authority.Of(ctx) is the owner, and that the shell belongs to the room.",
	"SyncFetch":                   "Checks git sync fetch with authority.Of(ctx).",
	"TemplateSeed":                "Requires a recognized authority.Of(ctx) role.",
	"TestHook":                    "actor(ctx) applies recordedCaller before running the hook.",
	"UpdateLibraryEntry":          "libraryStore passes mutationActor to library authority checks.",
	"VoiceSay":                    "voiceSay requires a recognized authority.Of(ctx) role.",
	"RoomMCPCall":                 "roommcp.Service.Call reads the caller from authority.Of(ctx) and checks the room and the grant.",
	"WakeLibrarian":               "librarianActor applies the recorded-caller policy.",
	"WakeLibrarianSmellsNow":      "librarianActor and the library wake now rule.",
	"WithdrawCall":                "callSender resolves the caller; notify checks the stored root.",
	"WithdrawQuestion":            "questionSender resolves the caller before withdrawing the question.",
}

// A Write binding must carry a caller policy on its Binding, apply its own
// in-method check, or remain named in the existing unchecked debt. Neither
// Local nor ScopeAdmin authenticates local sessions. The unchecked list may
// only shrink; new writes need a real caller policy.
func TestWriteCallsHaveCallerPolicy(t *testing.T) {
	// Inspect the real table without starting services or opening any stores.
	table := bindings(services{rooms: &roomsvc.Service{}})
	byName := map[string]websvc.Binding{}
	for _, binding := range table {
		byName[binding.Name] = binding
	}
	policies := map[string]string{}
	add := func(name, reason string) {
		t.Helper()
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("%s has no caller-policy reason", name)
		}
		if old, exists := policies[name]; exists {
			t.Fatalf("%s has two caller policies: %s; %s", name, old, reason)
		}
		policies[name] = reason
		binding, ok := byName[name]
		if !ok || binding.Access != websvc.Write {
			t.Errorf("caller policy for %s has no Write binding; remove its stale entry", name)
			return
		}
		if binding.Policy != 0 {
			t.Errorf("%s carries a binding policy and names its own check; keep one", name)
		}
	}
	for name, reason := range handlerCallerChecks {
		add(name, reason)
	}
	file, err := os.Open("testdata/unchecked-write-callers.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unchecked := 0
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, reason, ok := strings.Cut(line, " # ")
		if !ok || strings.ContainsAny(name, " \t") {
			t.Fatalf("bad unchecked-write entry %q; use Name # reason", line)
		}
		add(name, reason)
		unchecked++
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if unchecked > maxUncheckedWrites {
		t.Errorf("unchecked Write bindings grew to %d; at most %d allowed", unchecked, maxUncheckedWrites)
	}
	if unchecked < maxUncheckedWrites {
		t.Errorf("unchecked Write bindings shrank to %d; lower maxUncheckedWrites to %d", unchecked, unchecked)
	}
	for _, binding := range table {
		if binding.Access != websvc.Write || binding.Policy != 0 {
			continue
		}
		if _, ok := policies[binding.Name]; !ok {
			t.Errorf("write binding %s has no caller policy; protect it or identify its in-method check; the unchecked list may only shrink", binding.Name)
		}
	}
}
