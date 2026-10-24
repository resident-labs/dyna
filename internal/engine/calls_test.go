package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/devicesvc"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/gitsvc"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/store"
	"dyna-agent/internal/sysload"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/voice"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

func testServices(t *testing.T, readOnly bool) services {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := MigrateStores(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	rooms := roomsvc.New(nil)
	files := &filesvc.Service{Rooms: rooms.Notify.Rooms, SessionInRoom: rooms.SessionInRoom, AttachmentBytes: rooms.AttachmentBytes}
	terminals := terminalsvc.New(terminalOptions(rooms, 0))
	t.Cleanup(terminals.Shutdown)
	runtime := harnessruntime.New()
	runtime.SetLifetime(t.Context())
	return services{git: &gitsvc.Service{WorkspaceRoot: files.WorkspaceRoot, MaxBytes: filesvc.MaxWorkspaceBytes}, files: files, devices: devicesvc.New(rooms.Notify.Sessions.Store.Root), rooms: rooms, terminals: terminals, runtime: runtime, accounts: service.New(service.Options{Store: accountstore.DefaultStore()}), config: &configsvc.Service{ReadOnly: readOnly}, computers: newComputerHost(rooms), voice: &voice.Calls{}, hooks: hooks.New(), sync: lanesync.New(), health: engineHealth("", "", time.Now(), nil, hub.New(1), nil, nil), load: sysload.New()}
}

func testCalls(t *testing.T, readOnly bool) map[string]websvc.Call {
	t.Helper()
	calls, _, err := callsFor(testServices(t, readOnly), readOnly)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func TestCallTablePreservesBrowserAPI(t *testing.T) {
	calls := testCalls(t, false)
	for _, path := range []string{"../../web/src/api.ts", "../../web/src/lib/api/terminalTransport.ts"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range regexp.MustCompile("app[.]([A-Z][A-Za-z]+)[(]").FindAllStringSubmatch(string(source), -1) {
			if calls[match[1]] == nil {
				t.Errorf("browser API %s is not bound", match[1])
			}
		}
	}
	if _, err := calls["Terms"](t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

// Goal and question creation are bound for CLI transport. The browser
// still answers questions through AnswerQuestion.
func TestCallTableKeepsCLITransports(t *testing.T) {
	calls := testCalls(t, false)
	for _, name := range []string{"Ask"} {
		if calls[name] != nil {
			t.Errorf("the app can call %s", name)
		}
	}
	if calls["AddGoal"] == nil || calls["Goals"] == nil || calls["AnswerQuestion"] == nil {
		t.Fatal("goal transport and existing goal/question reads must remain bound")
	}
}

func TestReadOnlyCallTableRefusesWrites(t *testing.T) {
	s := testServices(t, true)
	calls, _, err := callsFor(s, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range bindings(s) {
		if entry.Access != websvc.Write {
			continue
		}
		name := entry.Name
		call, exists := calls[name]
		if !exists {
			t.Fatalf("write %s has no binding", name)
		}
		if _, err := call(t.Context(), nil); err == nil || err.Error() != "read-only mode" {
			t.Fatalf("%s write gate: %v", name, err)
		}
	}
	if _, err := calls["Rooms"](t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := calls["ConfigCheckProfile"](t.Context(), []json.RawMessage{json.RawMessage("\"missing\""), json.RawMessage("true")}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("live probe gate: %v", err)
	}
}

func TestHookActionsUseRequestPrincipal(t *testing.T) {
	services := testServices(t, false)
	t.Cleanup(authority.InEngine())
	calls, scopes, err := callsFor(services, false)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, raw)
	}
	if raw, err := exec.Command("git", "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, raw)
	}
	st, err := devicestore.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, dev, err := st.Pair(code, "Audit device")
	if err != nil {
		t.Fatal(err)
	}
	grants, err := devicegrant.Open(filepath.Dir(st.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Put("paired", dev.ID, scope.All(), false); err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{Store: st, Routes: websvc.LoopbackRoutes("7457", "7458"), Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}, Calls: calls, CallScopes: scopes, Hub: hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(method string, args ...any) {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7457/api/"+method, strings.NewReader(string(raw)))
		r = r.WithContext(authority.WithActor(r.Context(), authority.Actor{Role: authority.Owner}))
		r.Header.Set("Origin", "http://127.0.0.1:7457")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Dyna", "1")
		r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: token})
		w := httptest.NewRecorder()
		srv.AppHandler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
	}
	invoke("AddHook", repo, hooks.Hook{Name: "audit", Event: "schedule", Command: "true", EveryMinutes: 1, Cwd: "main", Timeout: "5s", OnFail: "alert"})
	list, err := hooks.New().List(context.Background(), repo)
	if err != nil || len(list) != 1 {
		t.Fatalf("hooks: %+v %v", list, err)
	}
	if list[0].AddedBy != "Audit device" {
		t.Fatalf("actor = %q", list[0].AddedBy)
	}
	command := "true"
	invoke("EditHook", repo, "audit", hooks.Patch{Command: &command})
	list, err = hooks.New().List(t.Context(), repo)
	if err != nil || list[0].EditedBy != "Audit device" {
		t.Fatalf("edit actor: %+v %v", list, err)
	}
	invoke("RunHook", repo, "audit")
	runs, err := hooks.New().Runs(t.Context(), repo, "audit")
	if err != nil || len(runs) != 1 || runs[0].RunnerSession != "Audit device" {
		t.Fatalf("run actor: %+v %v", runs, err)
	}
	copies, err := filepath.Glob(filepath.Join(store.DataDir(), "repos", "*", "runs", runs[0].ID+".json"))
	if err != nil || len(copies) != 0 {
		t.Fatalf("engine run wrote retired receipt copies: %v, %v", copies, err)
	}

}

func TestRuntimeConstructionAndUsageDoNotWriteAccounts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := accountstore.DefaultStore()
	// Write the historical fixture directly. Store.Update would add an
	// unrelated file and directory fsync to this startup test.
	fixture := accountstore.State{Version: 1, OpenCodeProvidersCleaned: true, Entries: []accountstore.Entry{
		{Metadata: accountstore.Metadata{ID: strings.Repeat("c", 32), Harness: "opencode", Integration: "opencode-go", Identity: "opencode:ref"}, Credential: accountstore.Credential{Schema: "opencode/v2-ref@1", Payload: json.RawMessage("{}")}},
	}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "accounts.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	checkReadOnly := func(name string, read func()) {
		t.Helper()
		// A fixed old timestamp detects rewrites even on coarse filesystems.
		stamp := time.Unix(946684800, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		read()
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Fatalf("%s changed account contents: %v", name, err)
		}
		again, err := os.Stat(path)
		if err != nil || !os.SameFile(info, again) || !again.ModTime().Equal(info.ModTime()) {
			t.Fatalf("%s rewrote account store: %v", name, err)
		}
	}
	accounts := service.New(service.Options{Store: store})
	checkReadOnly("old usage", func() { accounts.AccountUsage(t.Context(), false) })
	checkReadOnly("runtime construction", func() { newRuntime(t.Context()) })
}

func TestNotBuiltPageIsUsableShell(t *testing.T) {
	shell, err := fs.Sub(notBuilt, "notbuilt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(shell, "index.html")
	if err != nil || !strings.Contains(string(raw), "Frontend not built") || !strings.Contains(string(raw), "</head>") {
		t.Fatalf("fallback: %s %v", raw, err)
	}
}

// The table keeps the old request limits while Bind supplies the caller context.
func TestBindingPreservesRequestLimits(t *testing.T) {
	s := testServices(t, false)
	table := bindings(s)
	for _, tc := range []struct {
		name  string
		limit time.Duration
	}{
		{"NewWorktree", 10 * time.Minute}, {"GCWorktrees", 2 * time.Minute}, {"RemoveWorktrees", 2 * time.Minute},
		{"PruneWorktrees", 2 * time.Minute}, {"ApplyRunWorktreeDisposition", 2 * time.Minute},
		{"SendToSession", 30 * time.Second}, {"Send", 30 * time.Second}, {"Post", 30 * time.Second},
		{"StartRun", 30 * time.Second}, {"CancelRun", 30 * time.Second}, {"SteerRun", 30 * time.Second}, {"DeleteRoom", 30 * time.Second},
		{"StopSession", time.Minute}, {"BulkSessionAction", time.Minute}, {"CloseSession", time.Minute}, {"ForceCloseSession", time.Minute}, {"CloseSessionOptions", time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for i := range table {
				if table[i].Name != tc.name {
					continue
				}
				found = true
				// This test checks deadlines; TestLocalOnlyCallsAreMarked checks locality.
				table[i].Local = false
				table[i].Method = func(ctx context.Context) (time.Duration, error) {
					deadline, ok := ctx.Deadline()
					if !ok {
						return 0, errors.New("request has no deadline")
					}
					return time.Until(deadline), nil
				}
			}
			if !found {
				t.Fatalf("missing binding %s", tc.name)
			}
			calls, err := websvc.Bind(table)
			if err != nil {
				t.Fatal(err)
			}
			result, err := calls[tc.name](t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			left := result.(time.Duration)
			if left > tc.limit || left < tc.limit-time.Second {
				t.Fatalf("remaining limit = %s; want %s", left, tc.limit)
			}
			early, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err = calls[tc.name](early, nil)
			if err != nil {
				t.Fatal(err)
			}
			if left := result.(time.Duration); left > 5*time.Second || left < 4*time.Second {
				t.Fatalf("caller deadline changed: %s", left)
			}
		})
	}
}

func TestSendBindingUsesComposerArguments(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "composer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.rooms.Notify.Rooms.SetSeat(t.Context(), rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	// The composer names the conversation it writes in: the seat's DM.
	seat := room.SeatDMConversation(rec.ID)
	raw, err := json.Marshal([]any{rec.ID, "@root hello", "", []room.Attachment{}, "", string(seat)})
	if err != nil {
		t.Fatal(err)
	}
	var args []json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	result, err := calls["Send"](t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	sent, ok := result.(roomsvc.SendResult)
	if !ok || sent.Kind != "post" || sent.Post == nil || sent.Post.Message.Text != "hello" {
		t.Fatalf("send result=%+v", result)
	}
	if got := sent.Post.Message.Conversation; got != seat {
		t.Fatalf("post stored in %q, want the seat DM %q", got, seat)
	}
}

func TestSendBindingCarriesForwardSource(t *testing.T) {
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "forward"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.rooms.Notify.Rooms.SetSeat(t.Context(), rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	original, err := s.rooms.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{rec.Name}, Human: true, Text: "original result"})
	if err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]any{rec.ID, "@root please check", "", []room.Attachment{}, original.ID, ""})
	var args []json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	result, err := calls["Send"](t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	sent := result.(roomsvc.SendResult)
	if sent.Post.Message.Forward == nil || sent.Post.Message.Forward.ID != original.ID || sent.Post.Message.Forward.Text != original.Text {
		t.Fatalf("forwarded post = %+v", sent.Post.Message)
	}
}

func TestSystemLoadIsAReadCallAndNamesNoTopWithoutSamples(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		calls := testCalls(t, readOnly)
		if calls["SystemLoad"] == nil {
			t.Fatalf("SystemLoad has no binding (readOnly=%v)", readOnly)
		}
		got, err := calls["SystemLoad"](t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(got)
		for _, key := range []string{`"top":[]`, `"cpuPercent":null`, `"dynaKnown":false`} {
			if !strings.Contains(string(b), key) {
				t.Fatalf("SystemLoad = %s, want %s before any sample", b, key)
			}
		}
	}
}

func TestEveryCallHasOneScope(t *testing.T) {
	scopes := []struct {
		scope websvc.Scope
		names string
	}{
		{websvc.ScopeRead, `RunnerWaitInput Health SystemLoad Machines Machine Rooms FeedTail FeedAfter FeedAround SearchFeed OpenRoom Members MentionChoices Preview Transcript TranscriptMore RootSession HeldPRComments TranscriptPage TranscriptAfter TranscriptTurn Profile Profiles RoomSessionControls SessionTransfer SessionConsolidations RoomCIBudget CIHealth CIHealthRun CIHealthTest RoomChangePolicy Changes ChangeDetail MergeStatus MergeControlStatus RoomMergeStatus RoomMergeHistory ReviewerStatus ListServices GitHubList GitHubView GitHubDiff Runs RunDetail RunResult AttachmentText Pins Issues Library CallRequests Questions Visuals Rules Goals Goal GoalJournal GoalItems GoalItemCounts GoalStatus GoalEvents ListTemplates SessionQueue Notices AccountUsageCached UsageRollup ProfileUsage VoiceStatus PRSettings PRNoiseRules SyncStatus`},
		{websvc.ScopeOperate, "StampPlanAncestry AdmitNativeAttempt AddRunDirectories FinishRun ObserveMuseQuota ObserveClaudeUsage"},
		{websvc.ScopeOperate, `WakeLibrarian FileLibrarySmell AddLibraryEntry UpdateLibraryEntry LinkLibraryEntry RetireLibraryEntry NewWorktree GCWorktrees RemoveWorktrees PruneWorktrees ApplyRunWorktreeDisposition MarkRead MarkReadMany SendToSession SendWithReferences SendToSessionWithReferences Send Post StopSession BulkSessionAction PauseRoomSessions ResumeRoomSessions ClearDormantSessions DismissSession ReturnSeat CloseSession CloseSessionOptions ReopenSession ForceCloseSession PromoteSession TransferSession CancelSessionTransfer SubmitTransferHandoff ConsolidateSessions ResumeSessionConsolidation CancelSessionConsolidation ReplyToFinding SendDiffComments SendWorktreeComments RestartReviewer StartIssueWork DismissPRComment AdmitPRComment CommentPR PostPRReview LearnPRNoise UntrackPR ResumePR TrackPR TrackPRRef TransferPR SetPRDraft RequirePRReviewers CloseChange OpenChange RecordReviewResult RecordPRVerdict Attach Pin Unpin CloseIssue IssueGoal RequestCall WithdrawCall CallRequestShown DeclineCallRequest AskQuestion WithdrawQuestion AnswerQuestion AddGoal AddGoalItem MoveGoalItem EditGoalSelectors EditGoalItem EditGoal FinishGoal ExpediteGoal SetPRGoal SetGoalItemState NoteGoal RunnerSetPid RunnerSetRuntimePin RunnerSetInstructionsStale RunnerSetHarnessSession RunnerSetDMCursor RunnerFinish RunnerTurnStart RunnerTurnNote RunnerTurnEnd RunnerCompleteTurn RunnerReturnDormant RunnerInstructions RunnerClaimInput CancelRun PauseRun RemoveRun ClearRuns ArchiveRun UnarchiveRun SteerRun DeletePendingSteer DismissPendingPost DismissNotice VoiceStart VoiceResume VoiceStop AttachSeat AdoptSession RenameSession StartSubagent`},
		{websvc.ScopeFiles, `ListEntries ReadFile SyncFetch SyncRefresh GitExport GitStatus GitLastCommits GitReadFile GitHistory GitBlame GitDiff BrowseInfo BrowseDirs InspectPath`},
		{websvc.ScopeTerminal, `TermOpen Terms TermAttach TermWrite TermResize TermClose ShellJobs ShellLog ShellLogRange StopShell`},
		{websvc.ScopeComputers, `Computers Computer ComputerControl`},
		{websvc.ScopeAdmin, `EnableLibrarian DisableLibrarian SaveMachine ProbeMachine TemplateSeed StartRun SetRoomCIBudget SetMergeSettings SetReviewPublishing SetGitHubAccess MergeFixPR MergeControl SetMergePaused ResumeMerging SetRoomChangePolicy SetChangePolicyOverride SetReviewerProfile SetReviewEnabled SetServiceProfile ListHooks HookSummary HookRuns RunHook TestHook ReconcileHookInstall AddHook EditHook RemoveHook HookRunLog ListGlobalHooks GlobalHookSummary GlobalHookRuns RunGlobalHook AddGlobalHook EditGlobalHook RemoveGlobalHook ResetHook GlobalHookRunLog RemovePRNoiseRule SetPRSettings ExportIssue AddRule EditRule RemoveRule Devices RevokeDevice SetDeviceScopes CreatePairing ArchiveRoom UnarchiveRoom RenameRoom DeleteRoom PinRoom UnpinRoom ConfigCheckProfile ConfigProfiles UpsertProfile SetProfileUses SaveProfile RemoveProfile SetDefaultProfile SetProfileDisabled Settings SetSetting UpdateNotice UpdateDyna DataDir CreateRoom CreateRoomRecord RestartPrivateRuntimeLogin SwitchPrivateRuntimeAccount PrivateRuntimeSwitchState CancelPrivateRuntimeAccountSwitch ConfirmPrivateRuntimeAccountSwitch PreviewPrivateConfig ImportPrivateConfig PrivateRuntimes RefreshPrivateRuntimes RemovePrivateRuntime UninstallPrivateRuntime InstallPrivateRuntime BeginPrivateRuntimeLogin FindPersonalLogin UsePersonalLogin PrivateRuntimeLogins PrivateRuntimeLoginState RespondPrivateRuntimeLogin CancelPrivateRuntimeLogin FinishPrivateRuntimeLogin SetAccountRepos RemovePrivateRuntimeAccount HarnessProviderKeys SetHarnessConfigEnabled SyncCatalogAccount SyncPrivateRuntime ActivatePrivateRuntimeAccount GCPrivateRuntime AddCatalogMCP AddCatalogSkill RemoveCatalogEntry SetCatalogEntryEnabled AddHarnessMCP AddHarnessSkill RemoveHarnessConfig HarnessConfig AccountUsage ProfileModels`},
	}
	want := map[string]websvc.Scope{}
	for _, group := range scopes {
		for _, name := range strings.Fields(group.names) {
			if _, dup := want[name]; dup {
				t.Fatalf("%s is listed in more than one scope", name)
			}
			want[name] = group.scope
		}
	}
	got := map[string]websvc.Scope{}
	for _, b := range bindings(testServices(t, false)) {
		if _, dup := got[b.Name]; dup {
			t.Fatalf("%s is bound twice", b.Name)
		}
		got[b.Name] = b.Scope
		if _, listed := want[b.Name]; !listed {
			t.Errorf("%s has no scope in the expected table", b.Name)
		} else if b.Scope != want[b.Name] {
			t.Errorf("%s has scope %q, want %q", b.Name, b.Scope, want[b.Name])
		}
	}
	for name := range want {
		if _, bound := got[name]; !bound {
			t.Errorf("%s is in the expected table but is not bound", name)
		}
	}
}

func TestConditionalWritesAreRejectedInReadOnlyMode(t *testing.T) {
	s := testServices(t, true)
	calls, scopes, err := callsFor(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if scopes["AccountUsageCached"] != websvc.ScopeRead {
		t.Fatal("cached usage must be available with read scope")
	}
	for _, name := range []string{"ConfigCheckProfile", "UpdateNotice", "AccountUsage", "ProfileModels"} {
		if scopes[name] != websvc.ScopeAdmin {
			t.Fatalf("%s must retain admin scope", name)
		}
		if _, err := calls[name](t.Context(), nil); err == nil || err.Error() != "read-only mode" {
			t.Fatalf("%s: wanted read-only rejection before arguments or service execution, got %v", name, err)
		}
	}
	for _, entry := range bindings(s) {
		if entry.Access == websvc.Write && entry.Scope == websvc.ScopeRead {
			t.Errorf("%s is Write in read scope", entry.Name)
		}
	}
	if _, err := calls["AccountUsageCached"](t.Context(), nil); err != nil {
		t.Fatalf("cached usage unavailable: %v", err)
	}
}

func TestReadOnlyWebMethodsMatchesBindings(t *testing.T) {
	names := ReadOnlyWebMethods()
	got := make(map[string]bool, len(names))
	for _, name := range names {
		got[name] = true
	}
	for _, binding := range bindings(testServices(t, true)) {
		if got[binding.Name] != (binding.Access == websvc.Read) {
			t.Fatalf("%s: read catalog does not match Access metadata", binding.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("read catalog is empty")
	}
}

// QA lists the read methods from an agent shell, where the data folder would
// resolve into the harness runtime home and store.DataDir refuses it.
// Building the binding table must not resolve the data folder; a call does.
func TestReadOnlyWebMethodsResolvesNoDataFolder(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "accounts", "claude-code", ".runtime.home", ".local", "share"))
	if names := ReadOnlyWebMethods(); len(names) == 0 {
		t.Fatal("read catalog is empty")
	}
}

// Every call keeps the 5 s deadline unless callspec.LongCalls names it
// with a reason (item 8). A new long call must be added here on purpose.
func TestLongCallsAreBoundAndListedOnPurpose(t *testing.T) {
	// The catalog is static. Inspect its bindings without starting services.
	calls := make(map[string]bool)
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		calls[binding.Name] = true
	}
	for name, deadline := range callspec.LongCalls {
		if !calls[name] {
			t.Errorf("LongCalls names %s, which is not a call", name)
		}
		if deadline <= 5*time.Second {
			t.Errorf("LongCalls gives %s %v, which is not longer than 5 s", name, deadline)
		}
	}
	// VoiceResume can dial a provider, like VoiceStart.
	if n := len(callspec.LongCalls); n != 93 {
		t.Fatalf("LongCalls has %d calls, want 93; a call over 5 s needs a reason and a deliberate change here", n)
	}
}

// A dyna build from before PR #345 sends seven positional arguments to
// RecordPRVerdict and reads its notice. The shim keeps that request shape, so
// an older CLI never sees "takes 3 arguments, got 7" after an engine update.
func TestLegacyReviewVerdictCallKeepsItsOldRequestShape(t *testing.T) {
	calls := testCalls(t, false)
	args := []json.RawMessage{
		json.RawMessage(`"room"`), json.RawMessage(`"key"`), json.RawMessage(`"head"`),
		json.RawMessage(`"approve"`), json.RawMessage(`"Reviewed"`), json.RawMessage(`0`),
		json.RawMessage(`[{"severity":"nit","file":"a.go","line":4,"why":"Unclear","fix":"Rename it","replies":[]}]`),
	}
	_, err := calls["RecordPRVerdict"](t.Context(), args)
	// The call itself fails: the room in this fake request does not exist. What
	// must not fail is decoding the old request.
	if err == nil || strings.Contains(err.Error(), "arguments") || strings.Contains(err.Error(), "argument ") {
		t.Fatalf("the legacy review verdict request shape changed: %v", err)
	}
}

func TestHookWritesUseAuthorityInsteadOfEnvironment(t *testing.T) {
	s := testServices(t, false)
	t.Setenv("DYNA_ADDRESS", "forged:session")
	t.Setenv("DYNA_ROOM", "forged-room")
	s.hooks.Exec = func(_ context.Context, p processguard.Process) error {
		_, err := io.WriteString(p.Output, strings.Join(p.Env, "\n"))
		return err
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Initial"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	for i, tc := range []struct {
		name   string
		caller websvc.Caller
		actor  authority.Actor
		want   string
	}{
		{"owner", websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner}, "owner"},
		{"session", websvc.Caller{Kind: websvc.Session, Session: "ses_real"}, authority.Actor{Role: authority.Session, Session: "ses_real", Room: "room-real"}, "ses_real"},
		{"root", websvc.Caller{Kind: websvc.Session, Session: "ses_root"}, authority.Actor{Role: authority.Root, Session: "ses_root", Room: "room-root"}, "ses_root"},
		{"worker", websvc.Caller{Kind: websvc.Worker, Run: "wf_real", Agent: 2}, authority.Actor{Role: authority.Worker, Run: "wf_real", Agent: 2}, "run wf_real agent 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := localCallerContext(t, tc.caller, tc.actor)
			invokeFor := func(callCtx context.Context, name string, args ...any) any {
				t.Helper()
				raw := make([]json.RawMessage, len(args))
				for i, arg := range args {
					var err error
					raw[i], err = json.Marshal(arg)
					if err != nil {
						t.Fatal(err)
					}
				}
				result, err := calls[name](callCtx, raw)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return result
			}
			invoke := func(name string, args ...any) any { return invokeFor(ctx, name, args...) }
			editCtx, editor := ctx, tc.want
			if tc.actor.Role != authority.Owner && tc.actor.Role != authority.Root {
				// Sessions and workers may run hooks, but an owner must store them.
				editCtx = localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
				editor = "owner"
			}
			name := fmt.Sprintf("audit-%d", i)
			invokeFor(editCtx, "AddHook", repo, hooks.Hook{Name: name, Event: hooks.AfterLand, Command: "true", Cwd: "main", Timeout: "1s", OnFail: "alert", AddedBy: "forged", EditedBy: "forged"})
			command := "echo changed"
			invokeFor(editCtx, "EditHook", repo, name, hooks.Patch{Command: &command})
			list, err := s.hooks.List(ctx, repo)
			if err != nil || len(list) != 1 || list[0].AddedBy != editor || list[0].EditedBy != editor {
				t.Fatalf("audit: %+v %v", list, err)
			}
			for _, method := range []string{"RunHook", "TestHook"} {
				run := invoke(method, repo, name).(hooks.Run)
				if run.RunnerSession != tc.want {
					t.Fatalf("%s runner = %q, want %q", method, run.RunnerSession, tc.want)
				}
				log, err := os.ReadFile(run.LogPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(log), "DYNA_HOOK_SESSION="+tc.want) || strings.Contains(string(log), "sample:session") {
					t.Fatalf("%s identity: %s", method, log)
				}
				if method == "TestHook" && (!strings.Contains(string(log), "DYNA_HOOK_BRANCH=sample-branch") || !strings.Contains(string(log), "DYNA_HOOK_PR=1") || !strings.Contains(string(log), "DYNA_HOOK_SHA="+strings.Repeat("0", 40))) {
					t.Fatalf("sample values: %s", log)
				}
			}
			invokeFor(editCtx, "RemoveHook", repo, name)
		})
	}
}

// The web app pauses, resumes and cancels runs and stops shell jobs, so a
// paired device reaches these calls through its scopes.
func TestWebControlCallsReachPairedDevices(t *testing.T) {
	table := map[string]websvc.Binding{}
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		table[binding.Name] = binding
	}
	for name, scope := range map[string]websvc.Scope{"PauseRun": websvc.ScopeOperate, "CancelRun": websvc.ScopeOperate, "StopShell": websvc.ScopeTerminal} {
		binding, ok := table[name]
		if !ok || binding.Local || binding.Access != websvc.Write || binding.Scope != scope {
			t.Errorf("%s = %+v, %v; want a %s write call that is not local-only", name, binding, ok, scope)
		}
	}
}

// These calls delete, force or act only for a local process, so the call
// table keeps them on the private socket (websvc.Binding.Local).
func TestLocalOnlyCallsAreMarked(t *testing.T) {
	want := []string{"ObserveMuseQuota", "ObserveClaudeUsage", "AdmitNativeAttempt", "AddRunDirectories", "FinishRun", "SyncCatalogAccount", "AddCatalogMCP", "AddCatalogSkill", "RemoveCatalogEntry", "SetCatalogEntryEnabled", "UpsertProfile", "SetProfileUses", "EnableLibrarian", "DisableLibrarian", "RetireLibraryEntry", "OpenChange", "MergeFixPR", "MergeControl", "TransferPR", "TrackPRRef", "NewWorktree", "GCWorktrees", "RemoveWorktrees", "PruneWorktrees", "ApplyRunWorktreeDisposition", "StampPlanAncestry", "WithdrawQuestion", "AddGoal", "EditGoalSelectors", "SyncFetch", "TemplateSeed", "RequestCall", "WithdrawCall", "RemoveRun", "ClearRuns", "ArchiveRun", "UnarchiveRun", "CloseSessionOptions", "RunnerSetPid", "RunnerSetRuntimePin", "RunnerSetInstructionsStale", "RunnerSetHarnessSession", "RunnerSetDMCursor", "RunnerFinish", "RunnerTurnStart", "RunnerTurnNote", "RunnerTurnEnd", "RunnerCompleteTurn", "RunnerReturnDormant", "RunnerClaimInput", "RunnerWaitInput", "RunnerInstructions"}
	local := map[string]bool{}
	for _, binding := range bindings(services{rooms: &roomsvc.Service{}}) {
		local[binding.Name] = binding.Local
	}
	for _, name := range want {
		if !local[name] {
			t.Errorf("%s is not local-only", name)
		}
	}
}

func TestRequirePRReviewersUsesPRCallerGuard(t *testing.T) {
	var got authority.Actor
	calls := map[string]websvc.Call{
		"RequirePRReviewers": func(ctx context.Context, _ []json.RawMessage) (any, error) {
			got = authority.Of(ctx)
			return nil, nil
		},
	}
	protectPRCalls(calls)
	// A claimed session must not carry the process owner's authority into a PR write.
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
	claim := callspec.Claim{Session: "ordinary-session", Room: "work"}
	ctx = callspec.WithClaim(ctx, claim)
	if _, err := calls["RequirePRReviewers"](ctx, nil); err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("unverified require: %v", err)
	}
	want := authority.Actor{}
	if got != want {
		t.Fatalf("require caller = %+v, want %+v", got, want)
	}
}

func TestMergeControlUsesPRCallerGuard(t *testing.T) {
	for _, name := range []string{"MergeControl", "SetMergePaused"} {
		t.Run(name, func(t *testing.T) {
			var got authority.Actor
			calls := map[string]websvc.Call{
				name: func(ctx context.Context, _ []json.RawMessage) (any, error) {
					got = authority.Of(ctx)
					return nil, nil
				},
			}
			protectPRCalls(calls)
			// The shared policy refuses unverified callers before merge controls run.
			ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
			claim := callspec.Claim{Session: "ordinary-session", Room: "work"}
			ctx = callspec.WithClaim(ctx, claim)
			if _, err := calls[name](ctx, nil); err == nil || !strings.Contains(err.Error(), "could not verify") {
				t.Fatalf("unverified merge control: %v", err)
			}
			want := authority.Actor{}
			if got != want {
				t.Fatalf("merge caller = %+v, want %+v", got, want)
			}
		})
	}
}
