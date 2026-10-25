import type { Reference } from '../rooms/references.ts'
// The calls the web app sends to the engine. Each method is one entry of the
// call table in internal/engine/calls.go: the web client posts its arguments
// as a JSON array to /api/<Name>. A method that returns only an error in Go
// answers null. A return type of unknown means the Go result is checked by
// the reader that uses it (CIHealth). The harness config calls return Go's
// config.ConfigSnapshot, typed here as HarnessConfig, which lists the fields
// the web app reads (mcp and skills).
import type { HookInput } from '../settings/hooks.ts'
import type { ActivityRollup } from '../settings/usage/activity.ts'
import type { CallRequest } from '../voice/callRequests.ts'
import type { Catalog, UsageSnapshot } from './account.ts'
import type { ChangeDetail, ChangePolicy, ChangeView, HeldComment, MergeControlStatus, CIBudgetSettings, MergeHistoryEntry, MergeQueueItem, PolicyOverride, PRConfig, PRNoiseList, ReviewerStatus } from './change.ts'
import type { ComputerInfo } from './computer.ts'
import type { DiffComment } from '../files/diffComments.ts'
import type { Browse, BrowseInfo, CreatedRoom, CreateRoomInput, DirectoryEntries, GitBlameResult, GitDiffResult, GitHistoryResult, GitLastCommitsResult, GitStatusResult, PathCheck, WorkspaceFile } from './file.ts'
import type { GitHubDetail, GitHubListResult } from './github.ts'
import type { Goal, GoalEvent, GoalItem, GoalJournalEntry, GoalRule, GoalStatus, RulesView } from './goal.ts'
import type { HarnessConfig, HarnessSnapshot, ManagedMCP, ManagedSkill, PersonalLogin, PreferencePreview, PrivateLogin, PrivateRuntimeSwitchControl, ProviderKeysResult, SignInState } from './harness.ts'
import type { SyncStarted, SyncStatus } from './lanesync.ts'
import type { MachineDetail, MachinePatch, MachineSummary } from './machine.ts'
import type { LibraryView } from './library.ts'
import type { ProfileCheck, ProfileEdit, ProfileList, ProfileUsageSnapshot, ProfileView } from './profile.ts'
import type { Attachment, Draft, Feed, FeedSearch, Issue, Member, Opened, Post, PostResult, ProfileChoice, Question, Room, SeatReturn, SendResult, SpawnResult, Visual } from './room.ts'
import type { ServiceView } from './service.ts'
import type { BulkSessionOptions, BulkSessionResult, ConsolidationOptions, ConsolidationState, MemberProfile, QueueItem, RoomSessionMutation, RoomSessionState, Session, TransferOptions, TransferState } from './session.ts'
import type { Device, Hook, HookRun, HookRunLog, HookSummary, Notice, Pairing, SettingItem, UpdateNotice } from './settings.ts'
import type { ShellJob, ShellLog, ShellLogChunk, TermEvent, TermInfo } from './terminal.ts'
import type { Health, SystemLoad } from './system.ts'
import type { CallStatus } from './voice.ts'
import type { Page } from './wire.ts'
import type { RunDetail, RunList, RunResultView, RunSource, StartedRun, TemplateInfo } from './workflow.ts'

// ProfileSave is ProfileEdit with the env changes left out when there are none.
export type ProfileSave = Omit<ProfileEdit, 'envSet' | 'envRemove'> & Partial<Pick<ProfileEdit, 'envSet' | 'envRemove'>>

// AttachmentRef names a saved attachment on a send: its id and the name the
// composer shows. The engine reads the rest from the bytes it already holds.
export type AttachmentRef = Pick<Attachment, 'id' | 'name'>

export interface Engine {
  // Usage and models.
  AccountUsageCached(): Promise<UsageSnapshot>
  AccountUsage(refresh: boolean): Promise<UsageSnapshot>
  ProfileModels(harness: string, refresh: boolean): Promise<Catalog>
  UsageRollup(roomId: string, from: string, to: string): Promise<ActivityRollup>
  ProfileUsage(from: string, to: string): Promise<ProfileUsageSnapshot>

  // Questions, call requests and visuals.
  Questions(roomId: string): Promise<Record<string, Question[]>>
  CallRequests(roomId: string): Promise<Record<string, CallRequest[]>>
  CallRequestShown(roomId: string, id: string): Promise<CallRequest>
  DeclineCallRequest(roomId: string, id: string): Promise<CallRequest>
  Visuals(roomId: string): Promise<Visual[]>
  AnswerQuestion(roomId: string, id: string, choice: string, text: string): Promise<null>

  // Goals and rules.
  Goals(room: string, before: string, limit: number): Promise<Page<Goal>>
  Goal(room: string, id: string): Promise<Goal>
  GoalJournal(room: string, before: string, limit: number): Promise<Page<GoalJournalEntry>>
  GoalItems(room: string, id: string): Promise<GoalItem[] | null>
  GoalStatus(room: string, id: string): Promise<GoalStatus>
  GoalEvents(room: string, id: string, before: string, limit: number): Promise<Page<GoalEvent>>
  GoalItemCounts(room: string): Promise<Record<string, [number, number]> | null>
  EditGoal(room: string, id: string, text: string, holders: string[], shared: boolean): Promise<Goal>
  // AmendGoalBrief appends one attributed correction to the goal's brief.
  AmendGoalBrief(room: string, id: string, text: string): Promise<Goal>
  FinishGoal(room: string, id: string, summary: string, drop: boolean): Promise<Goal>
  ExpediteGoal(room: string, id: string, replace: boolean): Promise<Goal>
  SetPRGoal(room: string, key: string, goalId: string): Promise<null>
  Rules(room: string, global: boolean): Promise<RulesView>
  AddRule(room: string, global: boolean, text: string): Promise<GoalRule>
  EditRule(room: string, global: boolean, id: string, text: string): Promise<GoalRule>
  RemoveRule(room: string, global: boolean, id: string): Promise<GoalRule>

  // Files and git.
  ListEntries(roomRef: string, sessionRef: string, path: string, showHidden: boolean): Promise<DirectoryEntries>
  ReadFile(roomRef: string, sessionRef: string, path: string): Promise<WorkspaceFile>
  GitStatus(roomRef: string, sessionRef: string): Promise<GitStatusResult>
  GitLastCommits(roomRef: string, sessionRef: string, paths: string[]): Promise<GitLastCommitsResult>
  GitReadFile(roomRef: string, sessionRef: string, commit: string, path: string): Promise<WorkspaceFile>
  GitHistory(roomRef: string, sessionRef: string, path: string, before: string, limit: number): Promise<GitHistoryResult>
  GitBlame(roomRef: string, sessionRef: string, path: string): Promise<GitBlameResult>
  GitDiff(roomRef: string, sessionRef: string, path: string, staged: boolean): Promise<GitDiffResult>

  // Engine state.
  DataDir(): Promise<string>
  Health(): Promise<Health>
  SystemLoad(): Promise<SystemLoad>
  // Machines lists the registry; Machine reads one page; SaveMachine writes
  // the fields of MachinePatch and answers with the machine's next state.
  // ProbeMachine reads one machine now and answers with its page after it.
  Machines(): Promise<MachineSummary[]>
  Machine(id: string): Promise<MachineDetail>
  SaveMachine(id: string, patch: MachinePatch): Promise<MachineDetail>
  ProbeMachine(id: string): Promise<MachineDetail>
  SyncStatus(): Promise<SyncStatus>
  SyncRefresh(roomId: string, repo: string): Promise<SyncStarted>
  GitExport(roomId: string, repo: string): Promise<SyncStarted>

  // Rooms, feed and posts.
  Rooms(): Promise<Room[]>
  FeedTail(roomId: string, before: string, keep: number): Promise<Feed>
  FeedAfter(roomId: string, after: string, keep: number): Promise<Feed>
  FeedAround(roomId: string, id: string, keep: number): Promise<Feed>
  SearchFeed(roomId: string, query: string, limit: number): Promise<FeedSearch>
  OpenRoom(roomId: string, tail: number, withClosed: boolean): Promise<Opened>
  Members(roomId: string, withClosed: boolean): Promise<Member[]>
  MentionChoices(roomId: string): Promise<string[]>
  Pin(roomId: string, msgId: string): Promise<null>
  Unpin(roomId: string, msgId: string): Promise<null>
  Pins(roomId: string): Promise<Post[]>
  Preview(roomId: string, draft: string): Promise<Draft>
  MarkRead(roomId: string, messageId: string): Promise<null>
  MarkReadMany(roomId: string, ids: string[]): Promise<null>
  Send(roomId: string, draft: string, replyTo: string, atts: AttachmentRef[], forwardOf: string, conversation: string): Promise<SendResult>
  SendWithReferences(roomId: string, draft: string, replyTo: string, atts: AttachmentRef[], forwardOf: string, references: Reference[], conversation: string): Promise<SendResult>
  Attach(roomId: string, name: string, base64: string): Promise<Attachment>
  AttachmentText(roomId: string, id: string): Promise<string>
  ArchiveRoom(ref: string): Promise<null>
  UnarchiveRoom(ref: string): Promise<null>
  RenameRoom(ref: string, name: string): Promise<null>
  SetRoomRootProfile(ref: string, name: string): Promise<null>
  DeleteRoom(ref: string): Promise<null>
  PinRoom(ref: string, order: number): Promise<null>
  UnpinRoom(ref: string): Promise<null>

  // Issues and the library.
  Issues(roomId: string, all: boolean): Promise<Issue[]>
  Library(roomId: string): Promise<LibraryView>
  CloseIssue(roomId: string, id: string, reason: string): Promise<Issue>
  IssueGoal(roomId: string, id: string): Promise<Issue>
  ExportIssue(roomId: string, id: string): Promise<Issue>

  // Sessions.
  Transcript(ref: string): Promise<Session>
  RootSession(roomRef: string): Promise<Session>
  TranscriptPage(ref: string, before: string, limit: number): Promise<Session>
  TranscriptAfter(ref: string, after: string, limit: number): Promise<Session>
  TranscriptTurn(ref: string, turn: number): Promise<Session>
  Profile(ref: string): Promise<MemberProfile>
  SendToSession(ref: string, text: string, atts: AttachmentRef[], forwardOf: string): Promise<PostResult>
  Profiles(): Promise<ProfileChoice[]>
  StopSession(roomId: string, sessionId: string): Promise<Member>
  CloseSession(roomId: string, sessionId: string): Promise<Member>
  ForceCloseSession(roomId: string, sessionId: string): Promise<Member>
  ReopenSession(roomId: string, sessionId: string): Promise<Member>
  PromoteSession(roomId: string, sessionId: string): Promise<Member>
  DismissSession(roomId: string, sessionId: string): Promise<Member>
  ConsolidateSessions(roomId: string, options: ConsolidationOptions): Promise<ConsolidationState>
  SessionConsolidations(roomId: string): Promise<ConsolidationState[] | null>
  ResumeSessionConsolidation(roomId: string, id: string): Promise<ConsolidationState>
  CancelSessionConsolidation(roomId: string, id: string): Promise<ConsolidationState>
  RoomSessionControls(roomId: string): Promise<RoomSessionState>
  PauseRoomSessions(roomId: string): Promise<RoomSessionMutation>
  ResumeRoomSessions(roomId: string): Promise<RoomSessionMutation>
  ClearDormantSessions(roomId: string): Promise<RoomSessionMutation>
  ReturnSeat(roomId: string, handoff: string): Promise<SeatReturn>
  BulkSessionAction(roomId: string, options: BulkSessionOptions): Promise<BulkSessionResult>
  TransferSession(roomId: string, sessionId: string, options: TransferOptions): Promise<TransferState>
  SessionTransfer(roomId: string, sessionId: string): Promise<TransferState | null>
  CancelSessionTransfer(roomId: string, sessionId: string): Promise<TransferState>
  SessionQueue(ref: string): Promise<QueueItem[] | null>
  DeletePendingSteer(ref: string, id: string): Promise<null>
  DismissPendingPost(ref: string, id: string, confirmed: boolean): Promise<null>

  // Changes, pull requests and reviewers.
  Changes(roomRef: string, before: string, limit: number): Promise<Page<ChangeView>>
  ChangeDetail(roomRef: string, key: string): Promise<ChangeDetail>
  RoomMergeStatus(roomRef: string): Promise<MergeQueueItem[] | null>
  RoomMergeHistory(roomRef: string): Promise<MergeHistoryEntry[] | null>
  MergeControlStatus(roomRef: string): Promise<MergeControlStatus>
  SetMergePaused(roomRef: string, paused: boolean): Promise<null>
  ResumeMerging(roomRef: string): Promise<null>
  SendDiffComments(roomRef: string, changeId: string, comments: DiffComment[]): Promise<null>
  SendWorktreeComments(roomRef: string, session: string, comments: DiffComment[]): Promise<null>
  ReplyToFinding(roomRef: string, changeId: string, findingId: string, disposition: string, message: string): Promise<null>
  ReviewerStatus(roomRef: string): Promise<ReviewerStatus>
  SetReviewEnabled(roomRef: string, enabled: boolean): Promise<null>
  ListServices(roomRef: string): Promise<ServiceView[]>
  SetServiceProfile(roomRef: string, role: string, profile: string): Promise<null>
  RestartReviewer(roomRef: string): Promise<null>
  RoomCIBudget(roomId: string): Promise<CIBudgetSettings>
  SetRoomCIBudget(roomId: string, budget: CIBudgetSettings): Promise<null>
  RoomChangePolicy(roomId: string): Promise<ChangePolicy>
  SetRoomChangePolicy(roomId: string, policy: ChangePolicy): Promise<null>
  SetChangePolicyOverride(roomId: string, changeId: string, override: Partial<PolicyOverride>): Promise<null>
  PRSettings(roomRef: string): Promise<PRConfig>
  PRNoiseRules(roomRef: string): Promise<PRNoiseList>
  HeldPRComments(roomRef: string): Promise<HeldComment[]>
  AdmitPRComment(roomRef: string, key: string, id: string, version: string): Promise<null>
  DismissPRComment(roomRef: string, key: string, id: string): Promise<null>
  RemovePRNoiseRule(roomRef: string, id: string): Promise<null>
  SetPRSettings(roomRef: string, settings: PRConfig): Promise<PRConfig>
  UntrackPR(roomRef: string, key: string): Promise<null>
  ResumePR(roomRef: string, key: string): Promise<null>
  TrackPR(roomRef: string, sessionRef: string, number: number): Promise<ChangeView>
  GitHubList(roomRef: string, kind: string, state: string, search: string): Promise<GitHubListResult>
  GitHubView(roomRef: string, kind: string, number: number): Promise<GitHubDetail>
  StartIssueWork(roomRef: string, number: number): Promise<SpawnResult>
  GitHubDiff(roomRef: string, number: number): Promise<GitDiffResult>
  CIHealth(roomId: string, runs: number): Promise<unknown>
  CIHealthRun(roomId: string, token: string): Promise<unknown>
  CIHealthTest(roomId: string, test: string): Promise<unknown>

  // Workflow runs.
  ListTemplates(): Promise<TemplateInfo[]>
  StartRun(ref: string, source: RunSource, input: unknown): Promise<StartedRun>
  CancelRun(id: string): Promise<null>
  PauseRun(id: string, paused: boolean): Promise<null>
  SteerRun(id: string, agent: number, text: string): Promise<PostResult>
  Runs(roomId: string): Promise<RunList>
  RunDetail(id: string): Promise<RunDetail>
  RunResult(id: string, agent: number): Promise<RunResultView>

  // Devices.
  Devices(): Promise<Device[] | null>
  RevokeDevice(kind: string, id: string): Promise<number>
  SetDeviceScopes(id: string, scopes: string[]): Promise<Device>
  CreatePairing(origin: string): Promise<Pairing>

  // Terminals, computers and the Voice call.
  TermOpen(ref: string, cols: number, rows: number): Promise<TermInfo>
  Terms(): Promise<TermInfo[] | null>
  TermAttach(id: string, after: number): Promise<TermEvent>
  TermWrite(id: string, base64: string): Promise<null>
  TermResize(id: string, cols: number, rows: number): Promise<null>
  TermClose(id: string): Promise<null>
  Computers(roomId: string): Promise<ComputerInfo[]>
  Computer(session: string): Promise<ComputerInfo>
  ComputerControl(session: string, key: string, mode: string): Promise<string>
  VoiceStart(roomId: string): Promise<CallStatus>
  VoiceResume(roomId: string, startedAt: number, cursor: string): Promise<CallStatus>
  VoiceStatus(): Promise<CallStatus>
  VoiceStop(): Promise<null>
  ShellJobs(roomId: string, before: string, limit: number): Promise<Page<ShellJob>>
  StopShell(roomId: string, id: string): Promise<ShellJob>
  ShellLog(id: string, lines: number): Promise<ShellLog>
  ShellLogRange(id: string, offset: number, limit: number): Promise<ShellLogChunk>

  // Hooks.
  ListHooks(repo: string): Promise<Hook[] | null>
  HookSummary(repo: string): Promise<HookSummary[] | null>
  HookRuns(repo: string, name: string): Promise<HookRun[] | null>
  HookRunLog(repo: string, name: string, id: string): Promise<HookRunLog>
  RunHook(repo: string, name: string): Promise<HookRun>
  AddHook(repo: string, hook: HookInput): Promise<null>
  EditHook(repo: string, name: string, patch: Partial<HookInput>): Promise<null>
  RemoveHook(repo: string, name: string): Promise<null>
  // Global hooks belong to no repository: the built-ins the engine seeds and
  // the hooks the owner added for the whole machine.
  ListGlobalHooks(): Promise<Hook[] | null>
  GlobalHookSummary(): Promise<HookSummary[] | null>
  GlobalHookRuns(name: string): Promise<HookRun[] | null>
  GlobalHookRunLog(name: string, id: string): Promise<HookRunLog>
  RunGlobalHook(name: string): Promise<HookRun>
  AddGlobalHook(hook: HookInput): Promise<null>
  EditGlobalHook(name: string, patch: Partial<HookInput>): Promise<null>
  RemoveGlobalHook(name: string): Promise<null>
  ResetHook(name: string): Promise<null>

  // Profiles, settings and private runtimes.
  ConfigProfiles(): Promise<ProfileList>
  ConfigCheckProfile(name: string, live: boolean): Promise<ProfileCheck>
  SaveProfile(edit: ProfileSave): Promise<ProfileView>
  RemoveProfile(name: string): Promise<null>
  SetDefaultProfile(name: string): Promise<null>
  SetProfileDisabled(name: string, disabled: boolean): Promise<null>
  RestartPrivateRuntimeLogin(id: string, confirmed: boolean): Promise<PrivateLogin>
  PrivateRuntimeSwitchState(harness: string): Promise<PrivateRuntimeSwitchControl>
  CancelPrivateRuntimeAccountSwitch(harness: string, requestId: string): Promise<PrivateRuntimeSwitchControl>
  ConfirmPrivateRuntimeAccountSwitch(harness: string, requestId: string, stopConfirmed: boolean): Promise<PrivateRuntimeSwitchControl>
  SwitchPrivateRuntimeAccount(id: string, confirmed: boolean): Promise<HarnessSnapshot>
  PreviewPrivateConfig(id: string): Promise<PreferencePreview>
  ImportPrivateConfig(id: string, hash: string): Promise<HarnessSnapshot>
  PrivateRuntimes(): Promise<HarnessSnapshot>
  RefreshPrivateRuntimes(): Promise<HarnessSnapshot>
  RemovePrivateRuntime(harness: string, version: string, confirmed: boolean): Promise<HarnessSnapshot>
  UninstallPrivateRuntime(harness: string, confirmed: boolean): Promise<HarnessSnapshot>
  InstallPrivateRuntime(harness: string, version: string): Promise<HarnessSnapshot>
  BeginPrivateRuntimeLogin(harness: string, version: string, label: string, confirmed: boolean): Promise<PrivateLogin>
  FindPersonalLogin(harness: string, version: string): Promise<PersonalLogin | null>
  UsePersonalLogin(harness: string): Promise<HarnessSnapshot>
  PrivateRuntimeLogins(): Promise<PrivateLogin[] | null>
  PrivateRuntimeLoginState(id: string): Promise<SignInState>
  RespondPrivateRuntimeLogin(id: string, text: string): Promise<null>
  CancelPrivateRuntimeLogin(id: string): Promise<null>
  FinishPrivateRuntimeLogin(id: string): Promise<HarnessSnapshot>
  RemovePrivateRuntimeAccount(id: string, confirmed: boolean): Promise<HarnessSnapshot>
  HarnessProviderKeys(harness: string, action: string, provider: string, key: string): Promise<ProviderKeysResult>
  SetAccountRepos(id: string, repos: string[]): Promise<HarnessSnapshot>
  SetHarnessConfigEnabled(kind: string, name: string, harness: string, enabled: boolean): Promise<HarnessConfig>
  SyncPrivateRuntime(harness: string): Promise<HarnessSnapshot>
  AddHarnessMCP(entry: ManagedMCP): Promise<HarnessConfig>
  AddHarnessSkill(entry: ManagedSkill): Promise<HarnessConfig>
  RemoveHarnessConfig(kind: string, name: string, harness: string): Promise<HarnessConfig>
  HarnessConfig(): Promise<HarnessConfig>
  Settings(): Promise<SettingItem[] | null>
  SetSetting(key: string, value: string): Promise<SettingItem>
  UpdateNotice(): Promise<UpdateNotice>
  UpdateDyna(): Promise<null>
  Notices(): Promise<Notice[] | null>
  DismissNotice(id: string): Promise<null>

  // New room by path.
  BrowseInfo(): Promise<BrowseInfo>
  BrowseDirs(path: string): Promise<Browse>
  InspectPath(path: string): Promise<PathCheck>
  CreateRoom(input: CreateRoomInput): Promise<CreatedRoom>
}
