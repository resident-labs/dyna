// The web client calls the server API with JSON arguments.

import { createTransport } from './lib/api/transport.ts'
import { createTerminalTransport } from './lib/api/terminalTransport.ts'
import { openStream } from './lib/api/stream.ts'
import type { StreamSource } from './lib/api/stream.ts'
import { isPostResult, isRecord, isString } from './lib/api/guards.ts'
import type { AttachmentRef, Engine, ProfileSave } from './lib/api/engine.ts'
import type { MachinePatch } from './lib/api/machine.ts'
import type { ApprovalDecision } from './lib/api/approvals.ts'
import type { ChangePolicy, CIBudgetSettings, PolicyOverride, PRConfig } from './lib/api/change.ts'
import type { CreateRoomInput } from './lib/api/file.ts'
import type { ManagedMCP, ManagedSkill } from './lib/api/harness.ts'
import type { ServicePatch } from './lib/api/service.ts'
import type { Attachment, RoomLimitField, SetRoomLimitResult } from './lib/api/room.ts'
import type { BulkSessionOptions, ConsolidationOptions, TransferOptions } from './lib/api/session.ts'
import type { RunSource } from './lib/api/workflow.ts'
import type { CardRef } from './lib/api/wire.ts'
import { asComputerViewFailure } from './lib/api/computer.ts'
import type { DiffComment } from './lib/files/diffComments.ts'
import type { Reference } from './lib/rooms/references.ts'
import type { HookInput } from './lib/settings/hooks.ts'

// ConnectLink is the Resident Labs Connect link the engine puts in the page
// config. Source: Meta in internal/connect/service.go.
export interface ConnectLink {
  linked: boolean
  relay: string
  machine: string
  key: string
  name: string
}

// WebConfig is the page config the engine writes into the dyna-web meta tag.
// Source: ClientConfig in internal/websvc/server.go.
interface WebConfig {
  push: boolean
  connect: ConnectLink | null
  cardOrigin: string
}

function isConnectLink(v: unknown): v is ConnectLink {
  return isRecord(v) && typeof v.linked === 'boolean' && isString(v.relay) && isString(v.machine) && isString(v.key) && isString(v.name)
}

function readConfig(): WebConfig | null {
  try {
    const content = document.querySelector<HTMLMetaElement>('meta[name="dyna-web"]')?.content
    const raw: unknown = content ? JSON.parse(content) : null
    if (!isRecord(raw)) return null
    return { push: !!raw.push, connect: isConnectLink(raw.connect) ? raw.connect : null, cardOrigin: isString(raw.cardOrigin) ? raw.cardOrigin : '' }
  } catch {
    return null
  }
}

const web = readConfig()

// The server config controls which settings this client can show.
export const features = { push: !!web?.push }

// connect is the Resident Labs Connect link of this machine, or null.
export const connect = web?.connect ?? null

const { post, renew, authenticatedFetch, onUnauthorized, onRenewed, connectSession, connectHandoff, whoami, connectRoutes, pushStatus, pushSubscribe, pushNames, pushUnsubscribe, pushTest, pair, logout } = createTransport()
export { renew, authenticatedFetch, onUnauthorized, onRenewed, connectSession, connectHandoff, whoami, connectRoutes, pushStatus, pushSubscribe, pushNames, pushUnsubscribe, pushTest, pair, logout }

// The HTTP side of a call: app.Rooms() posts [] to /api/Rooms. The proxy is
// what gives every call table entry its typed arguments and result.
const callHandler: ProxyHandler<Engine> = { get: (_, name) => (...args: unknown[]) => post(`/api/${String(name)}`, args) }
const app = new Proxy({} as Engine, callHandler)

const { termWrite, termAttach } = createTerminalTransport(app, authenticatedFetch)
export { termAttach }

export interface SlashCommand {
  name: string
  source: 'workflow' | 'skill' | 'mcp-prompt'
  description: string
  args: { name: string; required: boolean; description: string }[]
}

export const api = {
  commands: async (room: string): Promise<SlashCommand[]> => {
    const response = await authenticatedFetch('/api/rooms/' + encodeURIComponent(room) + '/commands')
    if (!response.ok) throw new Error('Commands could not be loaded.')
    const value: unknown = await response.json()
    if (!Array.isArray(value)) throw new Error('The command list is invalid.')
    return value.filter((entry): entry is SlashCommand => isRecord(entry) && isString(entry.name) &&
      (entry.source === 'workflow' || entry.source === 'skill' || entry.source === 'mcp-prompt') &&
      isString(entry.description) && Array.isArray(entry.args) &&
      entry.args.every((arg: unknown) => isRecord(arg) && isString(arg.name) && typeof arg.required === 'boolean' && isString(arg.description)))
  },
  runCommand: async (room: string, name: string, text: string, target = '') => {
    const result = await post('/api/rooms/' + encodeURIComponent(room) + '/commands/run', { name, text, target })
    if (isRecord(result) && isRecord(result.run) && isString(result.run.id)) return { kind: 'run' as const, id: result.run.id }
    if (!isPostResult(result)) throw new Error('The command result is invalid.')
    return { kind: 'post' as const, post: result }
  },

  accountsView: (scope = '') => app.AccountsView(scope),
  accountsList: (scope = '', refresh = false) => app.AccountsList(scope, refresh),
  accountsUse: (id: string, scope = '') => app.AccountsUse(id, scope),
  accountsAdd: (harness: string, provider: string, label: string, key: string) => app.AccountsAdd(harness, provider, label, key),
  accountsRemove: (id: string) => app.AccountsRemove(id),
  profileModels: (harness: string, refresh = false) => app.ProfileModels(harness, refresh),
  usageRollup: (roomId = '', from: string, to: string) => app.UsageRollup(roomId, from, to),
  profileUsage: (from: string, to: string) => app.ProfileUsage(from, to),
  questions: (roomId: string) => app.Questions(roomId),
  callRequests: (roomId = '') => app.CallRequests(roomId),
  callRequestShown: (roomId: string, id: string) => app.CallRequestShown(roomId, id),
  declineCallRequest: (roomId: string, id: string) => app.DeclineCallRequest(roomId, id),
  visuals: (roomId: string) => app.Visuals(roomId),
  answerQuestion: (roomId: string, id: string, choice: string, text: string) => app.AnswerQuestion(roomId, id, choice, text),
  // The text came from an HTML card and the owner confirmed it.
  answerQuestionFromCard: (roomId: string, id: string, choice: string, text: string) => app.AnswerQuestionFromCard(roomId, id, choice, text),
  sendFromCard: (roomId: string, draft: string, replyTo: string, conversation: string) => app.SendFromCard(roomId, draft, replyTo, conversation),

  goals: (room = '', before = '', limit = 0) => app.Goals(room, before, limit),
  // One goal by ID, for links to goals no loaded page holds.
  goal: (room: string, id: string) => app.Goal(room, id),
  // The goal journal, newest first; before is a cursor.
  goalJournal: (room: string, before = '', limit = 0) => app.GoalJournal(room, before, limit),
  goalItems: (room: string, id: string) => app.GoalItems(room, id),
  goalStatus: (room: string, id: string) => app.GoalStatus(room, id),
  goalEvents: (room: string, id: string, before = '', limit = 0) => app.GoalEvents(room, id, before, limit),
  goalItemCounts: (room: string) => app.GoalItemCounts(room),
  editGoal: (room: string, id: string, text: string, holders: string[], shared: boolean) => app.EditGoal(room, id, text, holders, shared),
  // One append-only correction to the goal's brief; it keeps the original text.
  amendGoalBrief: (room: string, id: string, text: string) => app.AmendGoalBrief(room, id, text),
  finishGoal: (room: string, id: string, summary: string, drop = false) => app.FinishGoal(room, id, summary, drop),
  // An empty id stops the room's expedite; replace switches from another goal.
  expediteGoal: (room: string, id: string, replace = false) => app.ExpediteGoal(room, id, replace),
  // An empty goalId unlinks the change.
  setPRGoal: (room: string, key: string, goalId: string) => app.SetPRGoal(room, key, goalId),

  // global = true selects the rules for all rooms instead of this room's.
  rules: (room: string, global: boolean) => app.Rules(room, global),
  addRule: (room: string, global: boolean, text: string) => app.AddRule(room, global, text),
  editRule: (room: string, global: boolean, id: string, text: string) => app.EditRule(room, global, id, text),
  removeRule: (room: string, global: boolean, id: string) => app.RemoveRule(room, global, id),

  listEntries: (roomRef: string, sessionRef = '', path = '', showHidden = false) => app.ListEntries(roomRef, sessionRef, path, showHidden),
  readFile: (roomRef: string, sessionRef: string, path: string) => app.ReadFile(roomRef, sessionRef, path),
  gitStatus: (roomRef: string, sessionRef = '') => app.GitStatus(roomRef, sessionRef),
  gitLastCommits: (roomRef: string, sessionRef = '', paths: string[] = []) => app.GitLastCommits(roomRef, sessionRef, paths),
  gitReadFile: (roomRef: string, sessionRef: string, commit: string, path: string) => app.GitReadFile(roomRef, sessionRef, commit, path),
  gitHistory: (roomRef: string, sessionRef = '', path = '', before = '', limit = 50) => app.GitHistory(roomRef, sessionRef, path, before, limit),
  gitBlame: (roomRef: string, sessionRef = '', path = '') => app.GitBlame(roomRef, sessionRef, path),
  gitDiff: (roomRef: string, sessionRef = '', path = '', staged = false) => app.GitDiff(roomRef, sessionRef, path, staged),
  // The server reports its data directory.
  dataDir: () => app.DataDir(),
  health: () => app.Health(),
  systemLoad: () => app.SystemLoad(),
  // machines is the registry behind the Nodes list and the Node page.
  machines: () => app.Nodes(),
  machine: (id: string) => app.Node(id),
  machineWork: (id: string) => app.NodeWork(id),
  nodeHistory: (id: string) => app.NodeHistory(id),
  nodeLastDiagnosis: (id: string) => app.NodeLastDiagnosis(id),
  saveMachine: (id: string, patch: MachinePatch) => app.SaveNode(id, patch),
  probeMachine: (id: string) => app.ProbeNode(id),
  rooms: () => app.Rooms(),
  feedTail: (roomId: string, before = '', keep = 0) => app.FeedTail(roomId, before, keep),
  feedAfter: (roomId: string, after = '', keep = 50) => app.FeedAfter(roomId, after, keep),
  feedAround: (roomId: string, id: string, keep = 50) => app.FeedAround(roomId, id, keep),
  // searchFeed finds posts in the whole room log, newest first.
  searchFeed: (roomId: string, query: string, limit = 50) => app.SearchFeed(roomId, query, limit),
  // openRoom is a room's first screen in one call: the newest posts, members, choices, pins and runs.
  openRoom: (roomId: string, tail = 0, withClosed = false) => app.OpenRoom(roomId, tail, withClosed),
  members: (roomId: string, withClosed = false) => app.Members(roomId, withClosed),
  mentions: (roomId: string) => app.MentionChoices(roomId),
  pin: (roomId: string, msgId: string) => app.Pin(roomId, msgId),
  unpin: (roomId: string, msgId: string) => app.Unpin(roomId, msgId),
  pins: (roomId: string) => app.Pins(roomId),
  syncStatus: () => app.SyncStatus(),
  issues: (roomId: string, all = false) => app.Issues(roomId, all),
  library: (roomId: string) => app.Library(roomId),
  // ciHealth is the room CI graph and its slow tests; ciHealthTest opens one test.
  ciHealth: (roomId: string, runs = 30) => app.CIHealth(roomId, runs),
  ciHealthTest: (roomId: string, test: string) => app.CIHealthTest(roomId, test),
  closeIssue: (roomId: string, id: string, reason = '') => app.CloseIssue(roomId, id, reason),
  issueGoal: (roomId: string, id: string) => app.IssueGoal(roomId, id),
  exportIssue: (roomId: string, id: string) => app.ExportIssue(roomId, id),
  syncRefresh: (roomId: string, repo: string) => app.SyncRefresh(roomId, repo),
  gitExport: (roomId: string, repo: string) => app.GitExport(roomId, repo),
  preview: (roomId: string, draft: string) => app.Preview(roomId, draft),
  markRead: (roomId: string, messageId: string) => app.MarkRead(roomId, messageId),
  markReadMany: (roomId: string, ids: string[]) => app.MarkReadMany(roomId, ids),
  markRoomRead: (roomId: string) => app.MarkRoomRead(roomId),
  transcript: (ref: string) => app.Transcript(ref),
  rootSession: (roomRef: string) => app.RootSession(roomRef),
  transcriptPage: (ref: string, before = '', limit = 6) => app.TranscriptPage(ref, before, limit),
  transcriptAfter: (ref: string, after = '', limit = 6) => app.TranscriptAfter(ref, after, limit),
  transcriptTurn: (ref: string, turn: number) => app.TranscriptTurn(ref, turn),
  profile: (ref: string) => app.Profile(ref),
  sendToSession: (ref: string, text: string, atts: AttachmentRef[] = [], forwardOf = '') => app.SendToSession(ref, text, atts, forwardOf),
  // send is the composer's one send path: Go decides post or spawn. conversation
  // names the DM the client wrote in, and is empty for the room feed.
  send: (roomId: string, draft: string, replyTo = '', atts: AttachmentRef[] = [], forwardOf = '', references: Reference[] = [], conversation = '') => references.length ? app.SendWithReferences(roomId, draft, replyTo, atts, forwardOf, references, conversation) : app.Send(roomId, draft, replyTo, atts, forwardOf, conversation),
  profiles: () => app.Profiles(),
  stopSession: (roomId: string, sessionId: string) => app.StopSession(roomId, sessionId),
  consolidateSessions: (roomId: string, options: ConsolidationOptions) => app.ConsolidateSessions(roomId, options),
  sessionConsolidations: (roomId: string) => app.SessionConsolidations(roomId),
  resumeSessionConsolidation: (roomId: string, id: string) => app.ResumeSessionConsolidation(roomId, id),
  cancelSessionConsolidation: (roomId: string, id: string) => app.CancelSessionConsolidation(roomId, id),
  roomSessionControls: (roomId: string) => app.RoomSessionControls(roomId),
  pauseRoomSessions: (roomId: string) => app.PauseRoomSessions(roomId),
  resumeRoomSessions: (roomId: string) => app.ResumeRoomSessions(roomId),
  clearDormantSessions: (roomId: string) => app.ClearDormantSessions(roomId),
  dismissSession: (roomId: string, sessionId: string) => app.DismissSession(roomId, sessionId),
  returnSeat: (roomId: string) => app.ReturnSeat(roomId, ''),
  bulkSessionAction: (roomId: string, options: BulkSessionOptions) => app.BulkSessionAction(roomId, options),
  transferSession: (roomId: string, sessionId: string, options: TransferOptions) => app.TransferSession(roomId, sessionId, options),
  sessionTransfer: (roomId: string, sessionId: string) => app.SessionTransfer(roomId, sessionId),
  cancelSessionTransfer: (roomId: string, sessionId: string) => app.CancelSessionTransfer(roomId, sessionId),
  promoteSession: (roomId: string, sessionId: string) => app.PromoteSession(roomId, sessionId),
  reopenSession: (roomId: string, sessionId: string) => app.ReopenSession(roomId, sessionId),
  closeSession: (roomId: string, sessionId: string, force = false) => force ? app.ForceCloseSession(roomId, sessionId) : app.CloseSession(roomId, sessionId),
  changes: (roomRef = '', before = '', limit = 0) => app.Changes(roomRef, before, limit),
  changeDetail: (roomRef: string, key: string) => app.ChangeDetail(roomRef, key),
  roomMergeStatus: (roomRef: string) => app.RoomMergeStatus(roomRef),
  resumeMerging: (roomRef: string) => app.ResumeMerging(roomRef),
  mergeControlStatus: (roomRef: string) => app.MergeControlStatus(roomRef),
  setMergePaused: (roomRef: string, paused: boolean) => app.SetMergePaused(roomRef, paused),
  roomMergeHistory: (roomRef: string) => app.RoomMergeHistory(roomRef),
  sendDiffComments: (roomRef: string, changeId: string, comments: DiffComment[]) => app.SendDiffComments(roomRef, changeId, comments),
  sendWorktreeComments: (roomRef: string, session: string, comments: DiffComment[]) => app.SendWorktreeComments(roomRef, session, comments),
  replyToFinding: (roomRef: string, changeId: string, findingId: string, disposition: string, message: string) => app.ReplyToFinding(roomRef, changeId, findingId, disposition, message),
  reviewerStatus: (roomRef: string) => app.ReviewerStatus(roomRef),
  setReviewEnabled: (roomRef: string, enabled: boolean) => app.SetReviewEnabled(roomRef, enabled),
  listServices: (roomRef: string) => app.ListServices(roomRef),
  serviceRegistry: (roomRef: string, repo: string) => app.ServiceRegistry(roomRef, repo),
  setServiceProfile: (roomRef: string, role: string, profile: string) => app.SetServiceProfile(roomRef, role, profile),
  restartReviewer: (roomRef: string) => app.RestartReviewer(roomRef),
  roomCIBudget: (roomId: string) => app.RoomCIBudget(roomId),
  setRoomCIBudget: (roomId: string, budget: CIBudgetSettings) => app.SetRoomCIBudget(roomId, budget),
  roomChangePolicy: (roomId: string) => app.RoomChangePolicy(roomId),
  setRoomChangePolicy: (roomId: string, policy: ChangePolicy) => app.SetRoomChangePolicy(roomId, policy),
  setChangePolicyOverride: (roomId: string, changeId: string, override: Partial<PolicyOverride>) => app.SetChangePolicyOverride(roomId, changeId, override),
  prSettings: (roomRef: string) => app.PRSettings(roomRef),
  prNoiseRules: (roomRef: string) => app.PRNoiseRules(roomRef),
  heldPRComments: (roomRef = '') => app.HeldPRComments(roomRef),
  dismissPRComment: (roomRef: string, key: string, id: string) => app.DismissPRComment(roomRef, key, id),
  admitPRComment: (roomRef: string, key: string, id: string, version: string) => app.AdmitPRComment(roomRef, key, id, version),
  removePRNoiseRule: (roomRef: string, id: string) => app.RemovePRNoiseRule(roomRef, id),
  setPRSettings: (roomRef: string, settings: PRConfig) => app.SetPRSettings(roomRef, settings),
  untrackPR: (roomRef: string, key: string) => app.UntrackPR(roomRef, key),
  resumePR: (roomRef: string, key: string) => app.ResumePR(roomRef, key),
  trackPR: (roomRef: string, sessionRef: string, number: number) => app.TrackPR(roomRef, sessionRef, number),
  // The room repo's pull requests and issues, through gh on the host. kind
  // is 'pr' or 'issue'; state is open, closed, merged or all.
  githubList: (roomRef: string, kind: string, state = 'open', search = '') => app.GitHubList(roomRef, kind, state, search),
  githubView: (roomRef: string, kind: string, number: number) => app.GitHubView(roomRef, kind, number),
  trackedIssues: (roomRef: string) => app.TrackedIssues(roomRef),
  githubDiff: (roomRef: string, number: number) => app.GitHubDiff(roomRef, number),
  listWorkflows: (ref: string) => app.ListWorkflows(ref),
  startNamedRun: (room: string, name: string, input: unknown) => app.StartNamedRun({ room, name, input }),
  startRun: (ref: string, source: RunSource, input: unknown) => app.StartRun(ref, source, input),
  cancelRun: (id: string) => app.CancelRun(id),
  pauseRun: (id: string, paused: boolean) => app.PauseRun(id, paused),
  devices: () => app.Devices(),
  revokeDevice: (kind: string, id: string) => app.RevokeDevice(kind, id),
  setDeviceScopes: (id: string, scopes: string[]) => app.SetDeviceScopes(id, scopes),
  createPairing: (origin = '') => app.CreatePairing(origin),
  sessionQueue: (ref: string) => app.SessionQueue(ref),
  deletePendingSteer: (ref: string, id: string) => app.DeletePendingSteer(ref, id),
  dismissPendingPost: (ref: string, id: string, confirmed: boolean) => app.DismissPendingPost(ref, id, confirmed),
  archiveRoom: (ref: string) => app.ArchiveRoom(ref),
  unarchiveRoom: (ref: string) => app.UnarchiveRoom(ref),
  renameRoom: (ref: string, name: string) => app.RenameRoom(ref, name),
  setRoomRootProfile: (ref: string, name: string) => app.SetRoomRootProfile(ref, name),
  setRoomCheckIn: (ref: string, value: string) => app.SetRoomCheckIn(ref, value),
  setRoomLimit: (ref: string, field: RoomLimitField, value: string): Promise<SetRoomLimitResult> => app.SetRoomLimit(ref, field, value),
  deleteRoom: (ref: string) => app.DeleteRoom(ref),
  pinRoom: (ref: string, order = 0) => app.PinRoom(ref, order),
  unpinRoom: (ref: string) => app.UnpinRoom(ref),
  runs: (roomId: string) => app.Runs(roomId),
  runDetail: (id: string) => app.RunDetail(id),
  // agent 0 is the run result; a worker id gives that worker's result.
  runResult: (id: string, agent = 0) => app.RunResult(id, agent),
  termOpen: (ref: string, cols = 80, rows = 24) => app.TermOpen(ref, cols, rows),
  termRequestRun: (room: string, id: string, argv: string[] | null = null, cols = 80, rows = 24) => app.TermRequestRun(room, id, argv, cols, rows),
  approvalList: (room: string) => app.ApprovalList(room),
  // The engine returns the updated request; the card reads the feed's events instead.
  approvalDecide: (room: string, requestId: string, action: ApprovalDecision, reason = '') => app.ApprovalDecide({ room, requestId, action, reason }),
  termRequestReport: (room: string, id: string, verdict: 'done' | 'failed' | 'declined', note = '', shareLines = false) => app.TermRequestReport(room, id, verdict, note, shareLines),
  termRequestPreview: (room: string, id: string) => app.TermRequestPreview(room, id),
  terms: () => app.Terms(),
  termAttach,
  termWrite,
  termResize: (id: string, cols: number, rows: number) => app.TermResize(id, cols, rows),
  termClose: (id: string) => app.TermClose(id),
  // Computer pictures and WebSocket streams use the server origin.
  computers: (roomId: string) => app.Computers(roomId),
  computer: (session: string) => app.Computer(session),
  computerControl: (session: string, key: string, mode: string) => app.ComputerControl(session, key, mode),
  computerShotURL: (session: string, fresh = false) => `/api/computers/${encodeURIComponent(session)}/shot.png${fresh ? '?fresh=1' : ''}`,
  computerViewURL: async (session: string, key: string) => {
    const path = `/api/computers/${encodeURIComponent(session)}/view?k=${encodeURIComponent(key)}`
    // Browser WebSockets hide a refused handshake. Read permanent setup
    // failures over HTTP first; transient failures keep the old retry path.
    try {
      const response = await authenticatedFetch(`${path}&check=1`, { cache: 'no-store', headers: { 'X-Dyna': '1' } })
      if (!response.ok) {
        const failure = asComputerViewFailure(await response.json().catch(() => null))
        if (failure) throw failure
      }
    } catch (e) {
      const failure = asComputerViewFailure(e)
      if (failure) throw failure
    }
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${scheme}://${location.host}${path}`
  },
  // The owner's Voice call: the engine runs the gateway, and the page's
  // socket reaches it through the app origin.
  voiceStart: (roomId: string) => app.VoiceStart(roomId),
  voiceResume: (roomId: string, startedAt: number, cursor: string) => app.VoiceResume(roomId, startedAt, cursor),
  voiceStatus: () => app.VoiceStatus(),
  voiceStop: () => app.VoiceStop(),
  voiceURL: () => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/voice/realtime`,
  shellJobs: (roomId = '', before = '', limit = 0) => app.ShellJobs(roomId, before, limit),
  stopShell: (roomId: string, id: string) => app.StopShell(roomId, id),
  shellLog: (id: string, lines = 200) => app.ShellLog(id, lines),
  shellLogRange: (id: string, offset = 0, limit = 0) => app.ShellLogRange(id, offset, limit),
  listHooks: (repo: string) => app.ListHooks(repo),
  hookSummary: (repo: string) => app.HookSummary(repo),
  librarianDeferred: (repo: string) => app.LibrarianDeferred(repo),
  hookRuns: (repo: string, name: string) => app.HookRuns(repo, name),
  hookRunLog: (repo: string, name: string, id: string) => app.HookRunLog(repo, name, id),
  runHook: (repo: string, name: string) => app.RunHook(repo, name),
  addHook: (repo: string, hook: HookInput) => app.AddHook(repo, hook),
  editHook: (repo: string, name: string, patch: Partial<HookInput>) => app.EditHook(repo, name, patch),
  removeHook: (repo: string, name: string) => app.RemoveHook(repo, name),
  listGlobalHooks: () => app.ListGlobalHooks(),
  globalHookSummary: () => app.GlobalHookSummary(),
  globalHookRuns: (name: string) => app.GlobalHookRuns(name),
  globalHookRunLog: (name: string, id: string) => app.GlobalHookRunLog(name, id),
  runGlobalHook: (name: string) => app.RunGlobalHook(name),
  addGlobalHook: (hook: HookInput) => app.AddGlobalHook(hook),
  editGlobalHook: (name: string, patch: Partial<HookInput>) => app.EditGlobalHook(name, patch),
  removeGlobalHook: (name: string) => app.RemoveGlobalHook(name),
  editService: (name: string, patch: ServicePatch) => app.EditService(name, patch),
  resetService: (name: string) => app.ResetService(name),
  configProfiles: () => app.ConfigProfiles(),
  configCheckProfile: (name: string, live = false) => app.ConfigCheckProfile(name, live),
  saveProfile: (edit: ProfileSave) => app.SaveProfile(edit),
  removeProfile: (name: string) => app.RemoveProfile(name),
  setDefaultProfile: (name: string) => app.SetDefaultProfile(name),
  setProfileDisabled: (name: string, disabled: boolean) => app.SetProfileDisabled(name, disabled),
  restartPrivateRuntimeLogin: (id: string, confirmed = false) => app.RestartPrivateRuntimeLogin(id,confirmed),
 previewPrivateConfig: (id: string) => app.PreviewPrivateConfig(id),
 importPrivateConfig: (id: string, hash: string) => app.ImportPrivateConfig(id,hash),
 privateRuntimes: () => app.PrivateRuntimes(),
 refreshPrivateRuntimes: () => app.RefreshPrivateRuntimes(),
 removePrivateRuntime: (harness: string, version: string, confirmed = false) => app.RemovePrivateRuntime(harness,version,confirmed),
 uninstallPrivateRuntime: (harness: string) => app.UninstallPrivateRuntime(harness,true),
 installPrivateRuntime: (harness: string, version: string) => app.InstallPrivateRuntime(harness,version),
 beginPrivateRuntimeLogin: (harness: string, version: string, label: string, provider = '', confirmed = false) => app.BeginPrivateRuntimeLogin(harness,version,label,provider,confirmed),
 findPersonalLogin: (harness: string, version: string) => app.FindPersonalLogin(harness,version),
 usePersonalLogin: (harness: string) => app.UsePersonalLogin(harness),
 privateRuntimeLogins: () => app.PrivateRuntimeLogins(),
 privateRuntimeLoginState: (id: string) => app.PrivateRuntimeLoginState(id),
 respondPrivateRuntimeLogin: (id: string, text: string) => app.RespondPrivateRuntimeLogin(id,text),
 cancelPrivateRuntimeLogin: (id: string) => app.CancelPrivateRuntimeLogin(id),
 finishPrivateRuntimeLogin: (id: string) => app.FinishPrivateRuntimeLogin(id),
 setAccountRepos: (id: string, repos: string[]) => app.SetAccountRepos(id,repos),
 setHarnessConfigEnabled: (kind: string, name: string, harness: string, enabled: boolean) => app.SetHarnessConfigEnabled(kind,name,harness,enabled),
 syncPrivateRuntime: (harness: string) => app.SyncPrivateRuntime(harness),
 addHarnessMCP: (entry: ManagedMCP) => app.AddHarnessMCP(entry),
 addHarnessSkill: (entry: ManagedSkill) => app.AddHarnessSkill(entry),
 removeHarnessConfig: (kind: string, name: string, harness = "") => app.RemoveHarnessConfig(kind,name,harness),
 harnessConfig: () => app.HarnessConfig(),
  settings: () => app.Settings(),
  setSetting: (key: string, value: string) => app.SetSetting(key, value),
  updateNotice: () => app.UpdateNotice(),
  updateDyna: () => app.UpdateDyna(),
  // Dyna alerts: machine notices such as low disk space and hook or provider
  // failures. A {kind: "notices"} event means they changed.
  notices: () => app.Notices(),
  dismissNotice: (id: string) => app.DismissNotice(id),
  attach: (roomId: string, name: string, base64: string) => app.Attach(roomId, name, base64),
  attachmentText: (roomId: string, id: string) => app.AttachmentText(roomId, id),
  // New room by path: browseInfo is {home}, browseDirs is {parent, entries:
  // [{name, isGit}]}, inspectPath says what a typed path is, createRoom makes
  // the room or finds the repo's room.
  browseInfo: () => app.BrowseInfo(),
  browseDirs: (path: string) => app.BrowseDirs(path),
  inspectPath: (path: string) => app.InspectPath(path),
  createRoom: (input: CreateRoomInput) => app.CreateRoom(input),
}

// onEvent subscribes to the live stream: {kind: "posts"|"rooms"|"members"|
// "runs"|"pins", roomId, posts}. It sends {kind: "resync"} when events were
// lost, so the app reloads what it shows. See openStream for resume, the
// long-poll fallback and the silence rule. The second argument runs with
// false when the stream is lost and true when it is back, so the app can
// look for another route.
export function onEvent(fn: (ev: unknown) => void, state: (up: boolean) => void = () => {}) {
  return openStream(fn, state, {
    // EventSource has the members StreamSource lists; its handler types are wider.
    open: (url) => new EventSource(url) as StreamSource,
    poll: (url, signal) => authenticatedFetch(url, { cache: 'no-store', signal }),
    probe: () => post('/api/Health', []).catch(() => {}),
    wake: (fn) => {
      const shown = () => document.visibilityState === 'visible' && fn()
      window.addEventListener('online', fn)
      document.addEventListener('visibilitychange', shown)
      return () => {
        window.removeEventListener('online', fn)
        document.removeEventListener('visibilitychange', shown)
      }
    },
  })
}

// engineCheck asks the engine for its health. It returns the HTTP status,
// or 'network' when no answer came within 4 seconds. It cannot use post(),
// which keeps only the message of an error, not its status.
export async function engineCheck(): Promise<number | 'network'> {
  try {
    const r = await authenticatedFetch('/api/Health', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' }, body: '[]', signal: AbortSignal.timeout(4000) })
    return r.status
  } catch {
    return 'network'
  }
}

// copyText uses the browser clipboard.
export async function copyText(text: string) {
  await navigator.clipboard.writeText(text)
}

export function openExternal(url: string) {
  window.open(url, '_blank', 'noopener,noreferrer')
}

// fileURL is where the app serves a saved attachment's bytes.
export function fileURL(roomId: string, id: string) {
  return `/attachments/${roomId}/${id}`
}

// cardsIsolated is true when HTML cards come from their own origin (the
// web server's card listener), so card scripts reach nothing of the app.
export function cardsIsolated() {
  return !!web?.cardOrigin && web.cardOrigin !== location.origin
}

// cardURL resolves where an HTML card loads. Load it only in a sandboxed
// <iframe>: cards are untrusted agent HTML. The client asks for a URL on the card
// origin with a ticket that works for ten minutes; it is cached until a
// minute before it expires, so a feed of cards costs one call each.
const cards = new Map<string, CardRef>()
export function cardURL(roomId: string, id: string): Promise<string> {
  const key = `${roomId}/${id}`
  const hit = cards.get(key)
  if (hit && Date.parse(hit.expires) - Date.now() > 60000) return Promise.resolve(hit.url)
  return post('/api/CardURL', [roomId, id]).then((raw) => {
    if (!isCardRef(raw)) throw new Error('the card URL is missing')
    cards.set(key, raw)
    return raw.url
  })
}

// CardRef is the answer of /api/CardURL: where an HTML card loads and when
// its ticket ends.
function isCardRef(v: unknown): v is CardRef {
  return isRecord(v) && isString(v.url) && isString(v.expires)
}
