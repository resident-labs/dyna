// The web client calls the server API with JSON arguments.

import { deviceKey, makeProof } from './lib/settings/connect.ts'
import { openStream } from './lib/api/stream.ts'

function readConfig() {
  try {
    const c = document.querySelector('meta[name="dyna-web"]')?.content
    return c ? JSON.parse(c) : null
  } catch {
    return null
  }
}

const web = readConfig()

// The server config controls which settings this client can show.
export const features = { push: !!web?.push }

// connect is the Resident Labs Connect link of this machine, or null.
export const connect = web?.connect ?? null

let unauthorized = () => {}
// onUnauthorized runs when the server says this browser is not paired.
export function onUnauthorized(fn) {
  unauthorized = fn
}

// A Connect session has a short access window. A 401 with code "renew"
// means the session can renew: renew once (calls share one renewal), then
// send the call again once.
let renewing = null
let renewed = () => {}
// onRenewed runs with {expires, grantEnd} after each renewal.
export function onRenewed(fn) {
  renewed = fn
}

export function renew() {
  renewing ??= (async () => {
    try {
      const r = await fetch('/api/connect/renew', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' },
        body: JSON.stringify({ proof: await proof('/api/connect/renew') }),
      })
      // 401: the session has ended. 403: this browser's key cannot renew
      // it. Both mean sign-in; anything else (relay down, busy) may pass,
      // so the session stays and renewal tries again later.
      if (r.status === 401 || r.status === 403) unauthorized()
      const data = await r.json().catch(() => null)
      if (!r.ok) throw data?.error ?? `HTTP ${r.status}`
      renewed(data)
      return data
    } finally {
      renewing = null
    }
  })()
  return renewing
}

// Keep the response body available for binary previews and streams. Callers
// pass replayable bodies (JSON strings) and may cancel either attempt.
export async function authenticatedFetch(path, options = {}, canRenew = true) {
  const request = () => fetch(path, { ...options, credentials: 'same-origin' })
  let response = await request()
  if (response.status === 401) {
    const error = await response.clone().json().catch(() => null)
    if (error?.code === 'renew' && canRenew) {
      await renew()
      options.signal?.throwIfAborted()
      response = await request()
    }
    if (response.status === 401) unauthorized()
  }
  return response
}

// A room's feed is asked for again on every event and mostly comes back unchanged.
// The server tags that reply with an ETag; a POST gets no browser cache, so the
// last reply for each request is kept here and sent back with If-None-Match.
const REVALIDATED = new Set(['/api/FeedTail'])
const REVALIDATED_MAX = 32
const replies = new Map()

async function post(path, body, canRenew = true) {
  const payload = JSON.stringify(body)
  const key = REVALIDATED.has(path) ? path + payload : ''
  const known = key ? replies.get(key) : undefined
  const r = await authenticatedFetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Dyna': '1', ...(known && { 'If-None-Match': known.etag }) },
    body: payload,
  }, canRenew)
  if (r.status === 304 && known) return JSON.parse(known.text)
  // Only a revalidated reply keeps its raw text; every other call reads JSON as before.
  const text = key ? await r.text().catch(() => '') : ''
  const data = key ? (() => { try { return JSON.parse(text) } catch { return null } })() : await r.json().catch(() => null)
  // Errors reject with the server message.
  if (!r.ok) throw data?.error ?? `HTTP ${r.status}`
  const etag = key && r.headers.get('ETag')
  if (etag) {
    replies.delete(key)
    replies.set(key, { etag, text })
    if (replies.size > REVALIDATED_MAX) replies.delete(replies.keys().next().value)
  }
  return data
}

// The HTTP side of a call: app.Rooms() posts [] to /api/Rooms.
const app = new Proxy({}, { get: (_, name) => (...args) => post(`/api/${String(name)}`, args) })

// termWrite sends terminal input in order: one write per terminal at a
// time, and keys typed while a write is in flight join the next write. On
// the web each write is its own request, and two in flight can arrive at
// the host swapped. It resolves when the bytes reached the host.
const termQueues = new Map()
function termWrite(id, base64) {
  let q = termQueues.get(id)
  if (!q) {
    q = { pending: [], busy: false }
    termQueues.set(id, q)
  }
  const done = new Promise((resolve, reject) => q.pending.push({ base64, resolve, reject }))
  if (!q.busy) drainTerm(id, q)
  return done
}

async function drainTerm(id, q) {
  q.busy = true
  while (q.pending.length) {
    const batch = q.pending.splice(0)
    try {
      await app.TermWrite(id, btoa(batch.map((b) => atob(b.base64)).join('')))
      for (const b of batch) b.resolve()
    } catch (e) {
      for (const b of batch) b.reject(e)
    }
  }
  q.busy = false
  termQueues.delete(id)
}

export const api = {

  accountUsageCached: () => app.AccountUsageCached(),
  accountUsage: (refresh = false) => app.AccountUsage(refresh),
  profileModels: (harness, refresh = false) => app.ProfileModels(harness, refresh),
  usageRollup: (roomId = '', from, to) => app.UsageRollup(roomId, from, to),
  questions: (roomId) => app.Questions(roomId),
  callRequests: (roomId = '') => app.CallRequests(roomId),
  callRequestShown: (roomId, id) => app.CallRequestShown(roomId, id),
  declineCallRequest: (roomId, id) => app.DeclineCallRequest(roomId, id),
  visuals: (roomId) => app.Visuals(roomId),
  answerQuestion: (roomId, id, choice, text) => app.AnswerQuestion(roomId, id, choice, text),

  goals: (room = '', before = '', limit = 0) => app.Goals(room, before, limit),
  // One goal by ID, for links to goals no loaded page holds.
  goal: (room, id) => app.Goal(room, id),
  // The goal keeper's check-in journal, newest first; before is a cursor.
  goalJournal: (room, before = '', limit = 0) => app.GoalJournal(room, before, limit),
  goalItems: (room, id) => app.GoalItems(room, id),
  goalStatus: (room, id) => app.GoalStatus(room, id),
  goalEvents: (room, id, before = '', limit = 0) => app.GoalEvents(room, id, before, limit),
  goalItemCounts: (room) => app.GoalItemCounts(room),
  editGoal: (room, id, text, holders, shared) => app.EditGoal(room, id, text, holders, shared),
  finishGoal: (room, id, summary, drop = false) => app.FinishGoal(room, id, summary, drop),
  checkGoal: (room, id) => app.CheckGoal(room, id),
  // An empty id stops the room's expedite; replace switches from another goal.
  expediteGoal: (room, id, replace = false) => app.ExpediteGoal(room, id, replace),
  // An empty goalId unlinks the change.
  setPRGoal: (room, key, goalId) => app.SetPRGoal(room, key, goalId),

  // global = true selects the rules for all rooms instead of this room's.
  rules: (room, global) => app.Rules(room, global),
  addRule: (room, global, text) => app.AddRule(room, global, text),
  editRule: (room, global, id, text) => app.EditRule(room, global, id, text),
  removeRule: (room, global, id) => app.RemoveRule(room, global, id),

  listEntries: (roomRef, sessionRef = '', path = '', showHidden = false) => app.ListEntries(roomRef, sessionRef, path, showHidden),
  readFile: (roomRef, sessionRef, path) => app.ReadFile(roomRef, sessionRef, path),
  gitStatus: (roomRef, sessionRef = '') => app.GitStatus(roomRef, sessionRef),
  gitLastCommits: (roomRef, sessionRef = '', paths = []) => app.GitLastCommits(roomRef, sessionRef, paths),
  gitReadFile: (roomRef, sessionRef, commit, path) => app.GitReadFile(roomRef, sessionRef, commit, path),
  gitHistory: (roomRef, sessionRef = '', path = '', before = '', limit = 50) => app.GitHistory(roomRef, sessionRef, path, before, limit),
  gitBlame: (roomRef, sessionRef = '', path = '') => app.GitBlame(roomRef, sessionRef, path),
  gitDiff: (roomRef, sessionRef = '', path = '', staged = false) => app.GitDiff(roomRef, sessionRef, path, staged),
  // The server reports its data directory.
  dataDir: () => app.DataDir(),
  health: () => app.Health(),
  systemLoad: () => app.SystemLoad(),
  rooms: () => app.Rooms(),
  feedTail: (roomId, before = '', keep = 0) => app.FeedTail(roomId, before, keep),
  feedAfter: (roomId, after = '', keep = 50) => app.FeedAfter(roomId, after, keep),
  feedAround: (roomId, id, keep = 50) => app.FeedAround(roomId, id, keep),
  // searchFeed finds posts in the whole room log, newest first.
  searchFeed: (roomId, query, limit = 50) => app.SearchFeed(roomId, query, limit),
  // openRoom is a room's first screen in one call: the newest posts, members, choices, pins and runs.
  openRoom: (roomId, tail = 0, withClosed = false) => app.OpenRoom(roomId, tail, withClosed),
  members: (roomId, withClosed = false) => app.Members(roomId, withClosed),
  mentions: (roomId) => app.MentionChoices(roomId),
  pin: (roomId, msgId) => app.Pin(roomId, msgId),
  unpin: (roomId, msgId) => app.Unpin(roomId, msgId),
  pins: (roomId) => app.Pins(roomId),
  syncStatus: () => app.SyncStatus(),
  issues: (roomId, all = false) => app.Issues(roomId, all),
  library: (roomId) => app.Library(roomId),
  // ciHealth is the room CI graph and its slow tests; the other two open a run and a test.
  ciHealth: (roomId, runs = 30) => app.CIHealth(roomId, runs),
  ciHealthRun: (roomId, token) => app.CIHealthRun(roomId, token),
  ciHealthTest: (roomId, test) => app.CIHealthTest(roomId, test),
  closeIssue: (roomId, id, reason = '') => app.CloseIssue(roomId, id, reason),
  issueGoal: (roomId, id, holder = '') => app.IssueGoal(roomId, id, holder),
  exportIssue: (roomId, id) => app.ExportIssue(roomId, id),
  syncRefresh: (roomId, repo) => app.SyncRefresh(roomId, repo),
  gitExport: (roomId, repo) => app.GitExport(roomId, repo),
  preview: (roomId, draft) => app.Preview(roomId, draft),
  markRead: (roomId, messageId) => app.MarkRead(roomId, messageId),
  markReadMany: (roomId, ids) => app.MarkReadMany(roomId, ids),
  transcript: (ref) => app.Transcript(ref),
  rootSession: (roomRef) => app.RootSession(roomRef),
  transcriptPage: (ref, before = '', limit = 6) => app.TranscriptPage(ref, before, limit),
  transcriptAfter: (ref, after = '', limit = 6) => app.TranscriptAfter(ref, after, limit),
  transcriptTurn: (ref, turn) => app.TranscriptTurn(ref, turn),
  profile: (ref) => app.Profile(ref),
  sendToSession: (ref, text, atts = [], forwardOf = '') => app.SendToSession(ref, text, atts, forwardOf),
  // send is the composer's one send path: Go decides post or spawn.
  send: (roomId, draft, replyTo = '', atts = [], forwardOf = '') => app.Send(roomId, draft, replyTo, atts, forwardOf),
  profiles: () => app.Profiles(),
  stopSession: (roomId, sessionId) => app.StopSession(roomId, sessionId),
  consolidateSessions: (roomId, options) => app.ConsolidateSessions(roomId, options),
  sessionConsolidations: (roomId) => app.SessionConsolidations(roomId),
  resumeSessionConsolidation: (roomId, id) => app.ResumeSessionConsolidation(roomId, id),
  cancelSessionConsolidation: (roomId, id) => app.CancelSessionConsolidation(roomId, id),
  roomSessionControls: (roomId) => app.RoomSessionControls(roomId),
  pauseRoomSessions: (roomId) => app.PauseRoomSessions(roomId),
  resumeRoomSessions: (roomId) => app.ResumeRoomSessions(roomId),
  clearDormantSessions: (roomId) => app.ClearDormantSessions(roomId),
  dismissSession: (roomId, sessionId) => app.DismissSession(roomId, sessionId),
  returnSeat: (roomId) => app.ReturnSeat(roomId, ''),
  bulkSessionAction: (roomId, options) => app.BulkSessionAction(roomId, options),
  transferSession: (roomId, sessionId, options) => app.TransferSession(roomId, sessionId, options),
  sessionTransfer: (roomId, sessionId) => app.SessionTransfer(roomId, sessionId),
  cancelSessionTransfer: (roomId, sessionId) => app.CancelSessionTransfer(roomId, sessionId),
  promoteSession: (roomId, sessionId) => app.PromoteSession(roomId, sessionId),
  reopenSession: (roomId, sessionId) => app.ReopenSession(roomId, sessionId),
  closeSession: (roomId, sessionId, force = false) => force ? app.ForceCloseSession(roomId, sessionId) : app.CloseSession(roomId, sessionId),
  changes: (roomRef = '', before = '', limit = 0) => app.Changes(roomRef, before, limit),
  changeDetail: (roomRef, key) => app.ChangeDetail(roomRef, key),
  roomMergeStatus: (roomRef) => app.RoomMergeStatus(roomRef),
  resumeMerging: (roomRef) => app.MergeControl(roomRef, 'resume', [], []),
  mergeControlStatus: (roomRef) => app.MergeControlStatus(roomRef),
  roomMergeHistory: (roomRef) => app.RoomMergeHistory(roomRef),
  sendDiffComments: (roomRef, changeId, comments) => app.SendDiffComments(roomRef, changeId, comments),
  sendWorktreeComments: (roomRef, session, comments) => app.SendWorktreeComments(roomRef, session, comments),
  replyToFinding: (roomRef, changeId, findingId, disposition, message) => app.ReplyToFinding(roomRef, changeId, findingId, disposition, message),
  reviewerStatus: (roomRef) => app.ReviewerStatus(roomRef),
  setReviewEnabled: (roomRef, enabled) => app.SetReviewEnabled(roomRef, enabled),
  listServices: (roomRef) => app.ListServices(roomRef),
  setServiceProfile: (roomRef, role, profile) => app.SetServiceProfile(roomRef, role, profile),
  restartReviewer: (roomRef) => app.RestartReviewer(roomRef),
  roomCIBudget: (roomId) => app.RoomCIBudget(roomId),
  setRoomCIBudget: (roomId, budget) => app.SetRoomCIBudget(roomId, budget),
  roomChangePolicy: (roomId) => app.RoomChangePolicy(roomId),
  setRoomChangePolicy: (roomId, policy) => app.SetRoomChangePolicy(roomId, policy),
  setChangePolicyOverride: (roomId, changeId, override) => app.SetChangePolicyOverride(roomId, changeId, override),
  prSettings: (roomRef) => app.PRSettings(roomRef),
  prNoiseRules: (roomRef) => app.PRNoiseRules(roomRef),
  heldPRComments: (roomRef = '') => app.HeldPRComments(roomRef),
  dismissPRComment: (roomRef, key, id) => app.DismissPRComment(roomRef, key, id),
  admitPRComment: (roomRef, key, id, version) => app.AdmitPRComment(roomRef, key, id, version),
  removePRNoiseRule: (roomRef, id) => app.RemovePRNoiseRule(roomRef, id),
  setPRSettings: (roomRef, settings) => app.SetPRSettings(roomRef, settings),
  untrackPR: (roomRef, key) => app.UntrackPR(roomRef, key),
  resumePR: (roomRef, key) => app.ResumePR(roomRef, key),
  trackPR: (roomRef, sessionRef, number) => app.TrackPR(roomRef, sessionRef, number),
  // The room repo's pull requests and issues, through gh on the host. kind
  // is 'pr' or 'issue'; state is open, closed, merged or all.
  githubList: (roomRef, kind, state = 'open', search = '') => app.GitHubList(roomRef, kind, state, search),
  githubView: (roomRef, kind, number) => app.GitHubView(roomRef, kind, number),
  startIssueWork: (roomRef, number) => app.StartIssueWork(roomRef, number),
  githubDiff: (roomRef, number) => app.GitHubDiff(roomRef, number),
  listTemplates: () => app.ListTemplates(),
  startRun: (ref, source, input) => app.StartRun(ref, source, input),
  cancelRun: (id) => app.CancelRun(id),
  pauseRun: (id, paused) => app.PauseRun(id, paused),
  steerRun: (id, agent, text) => app.SteerRun(id, agent, text),
  devices: () => app.Devices(),
  revokeDevice: (kind, id) => app.RevokeDevice(kind, id),
  setDeviceScopes: (id, scopes) => app.SetDeviceScopes(id, scopes),
  createPairing: (origin = '') => app.CreatePairing(origin),
  sessionQueue: (ref) => app.SessionQueue(ref),
  deletePendingSteer: (ref, id) => app.DeletePendingSteer(ref, id),
  dismissPendingPost: (ref, id, confirmed) => app.DismissPendingPost(ref, id, confirmed),
  archiveRoom: (ref) => app.ArchiveRoom(ref),
  unarchiveRoom: (ref) => app.UnarchiveRoom(ref),
  renameRoom: (ref, name) => app.RenameRoom(ref, name),
  deleteRoom: (ref) => app.DeleteRoom(ref),
  pinRoom: (ref, order = 0) => app.PinRoom(ref, order),
  unpinRoom: (ref) => app.UnpinRoom(ref),
  runs: (roomId) => app.Runs(roomId),
  runDetail: (id) => app.RunDetail(id),
  // agent 0 is the run result; a worker id gives that worker's result.
  runResult: (id, agent = 0) => app.RunResult(id, agent),
  termOpen: (ref, cols = 80, rows = 24) => app.TermOpen(ref, cols, rows),
  terms: () => app.Terms(),
  termAttach,
  termWrite,
  termResize: (id, cols, rows) => app.TermResize(id, cols, rows),
  termClose: (id) => app.TermClose(id),
  // Computer pictures and WebSocket streams use the server origin.
  computers: (roomId) => app.Computers(roomId),
  computer: (session) => app.Computer(session),
  computerControl: (session, key, mode) => app.ComputerControl(session, key, mode),
  computerShotURL: (session, fresh = false) => `/api/computers/${encodeURIComponent(session)}/shot.png${fresh ? '?fresh=1' : ''}`,
  computerViewURL: async (session, key) => {
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
    return `${scheme}://${location.host}/api/computers/${encodeURIComponent(session)}/view?k=${encodeURIComponent(key)}`
  },
  // The owner's Voice call: the engine runs the gateway, and the page's
  // socket reaches it through the app origin.
  voiceStart: (roomId) => app.VoiceStart(roomId),
  voiceResume: (roomId, startedAt, cursor) => app.VoiceResume(roomId, startedAt, cursor),
  voiceStatus: () => app.VoiceStatus(),
  voiceStop: () => app.VoiceStop(),
  voiceURL: () => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/voice/realtime`,
  shellJobs: (roomId = '', before = '', limit = 0) => app.ShellJobs(roomId, before, limit),
  stopShell: (roomId, id) => app.StopShell(roomId, id),
  shellLog: (id, lines = 200) => app.ShellLog(id, lines),
  shellLogRange: (id, offset = 0, limit = 0) => app.ShellLogRange(id, offset, limit),
  hookRepos: () => app.HookRepos(),
  listHooks: (repo) => app.ListHooks(repo),
  hookRuns: (repo, name) => app.HookRuns(repo, name),
  hookRunLog: (repo, name, id) => app.HookRunLog(repo, name, id),
  runHook: (repo, name) => app.RunHook(repo, name),
  addHook: (repo, hook) => app.AddHook(repo, hook),
  editHook: (repo, name, patch) => app.EditHook(repo, name, patch),
  removeHook: (repo, name) => app.RemoveHook(repo, name),
  configProfiles: () => app.ConfigProfiles(),
  configCheckProfile: (name, live = false) => app.ConfigCheckProfile(name, live),
  saveProfile: (edit) => app.SaveProfile(edit),
  removeProfile: (name) => app.RemoveProfile(name),
  setDefaultProfile: (name) => app.SetDefaultProfile(name),
  setProfileDisabled: (name, disabled) => app.SetProfileDisabled(name, disabled),
  restartPrivateRuntimeLogin: (id,confirmed=false) => app.RestartPrivateRuntimeLogin(id,confirmed),
 privateRuntimeSwitchState: (harness) => app.PrivateRuntimeSwitchState(harness),
 cancelPrivateRuntimeAccountSwitch: (harness,requestId) => app.CancelPrivateRuntimeAccountSwitch(harness,requestId),
 confirmPrivateRuntimeAccountSwitch: (harness,requestId,stopConfirmed=false) => app.ConfirmPrivateRuntimeAccountSwitch(harness,requestId,stopConfirmed),
 switchPrivateRuntimeAccount: (id,confirmed) => app.SwitchPrivateRuntimeAccount(id,confirmed),
 previewPrivateConfig: (id) => app.PreviewPrivateConfig(id),
 importPrivateConfig: (id,hash) => app.ImportPrivateConfig(id,hash),
 privateRuntimes: () => app.PrivateRuntimes(),
 refreshPrivateRuntimes: () => app.RefreshPrivateRuntimes(),
 removePrivateRuntime: (harness,version,confirmed=false) => app.RemovePrivateRuntime(harness,version,confirmed),
 uninstallPrivateRuntime: (harness) => app.UninstallPrivateRuntime(harness,true),
 installPrivateRuntime: (harness,version) => app.InstallPrivateRuntime(harness,version),
 beginPrivateRuntimeLogin: (harness,version,label,confirmed=false) => app.BeginPrivateRuntimeLogin(harness,version,label,confirmed),
 findPersonalLogin: (harness,version) => app.FindPersonalLogin(harness,version),
 usePersonalLogin: (harness) => app.UsePersonalLogin(harness),
 privateRuntimeLogins: () => app.PrivateRuntimeLogins(),
 privateRuntimeLoginState: (id) => app.PrivateRuntimeLoginState(id),
 respondPrivateRuntimeLogin: (id,text) => app.RespondPrivateRuntimeLogin(id,text),
 cancelPrivateRuntimeLogin: (id) => app.CancelPrivateRuntimeLogin(id),
 finishPrivateRuntimeLogin: (id) => app.FinishPrivateRuntimeLogin(id),
 removePrivateRuntimeAccount: (id,confirmed) => app.RemovePrivateRuntimeAccount(id,confirmed),
 harnessProviderKeys: (harness,action,provider="",key="") => app.HarnessProviderKeys(harness,action,provider,key),
 setAccountRepos: (id,repos) => app.SetAccountRepos(id,repos),
 setHarnessConfigEnabled: (kind,name,harness,enabled) => app.SetHarnessConfigEnabled(kind,name,harness,enabled),
 syncPrivateRuntime: (harness) => app.SyncPrivateRuntime(harness),
 addHarnessMCP: (entry) => app.AddHarnessMCP(entry),
 addHarnessSkill: (entry) => app.AddHarnessSkill(entry),
 removeHarnessConfig: (kind,name,harness="") => app.RemoveHarnessConfig(kind,name,harness),
 harnessConfig: () => app.HarnessConfig(),
  settings: () => app.Settings(),
  setSetting: (key, value) => app.SetSetting(key, value),
  updateNotice: () => app.UpdateNotice(),
  updateDyna: () => app.UpdateDyna(),
  // Dyna alerts: machine notices such as low disk space and hook or provider
  // failures. A {kind: "notices"} event means they changed.
  notices: () => app.Notices(),
  dismissNotice: (id) => app.DismissNotice(id),
  attach: (roomId, name, base64) => app.Attach(roomId, name, base64),
  attachmentText: (roomId, id) => app.AttachmentText(roomId, id),
  // New room by path: browseInfo is {home}, browseDirs is {parent, entries:
  // [{name, isGit}]}, inspectPath says what a typed path is, createRoom makes
  // the room or finds the repo's room.
  browseInfo: () => app.BrowseInfo(),
  browseDirs: (path) => app.BrowseDirs(path),
  inspectPath: (path) => app.InspectPath(path),
  createRoom: (input) => app.CreateRoom(input),
}

// proof signs a request to path with this browser's device key.
async function proof(path) {
  return makeProof(await deviceKey(), 'POST', location.origin + path)
}

// connectSession trades a relay code or a route handoff for a session.
// secret is {code} or {handoff}.
export async function connectSession(secret) {
  return post('/api/connect/session', { ...secret, proof: await proof('/api/connect/session') })
}

// connectHandoff asks for a one-time code to open a session on another route.
export async function connectHandoff(target) {
  return post('/api/connect/handoff', { target, proof: await proof('/api/connect/handoff') })
}

export const whoami = () => post('/api/connect/whoami', [])
export const connectRoutes = () => post('/api/connect/routes', [])

// Web Push: the server holds the keys and sends; the browser subscribes.
export const pushStatus = () => post('/api/push/status', [])
export const pushSubscribe = (sub, names = false) => post('/api/push/subscribe', { endpoint: sub.endpoint, keys: sub.keys, names })
export const pushNames = (names) => post('/api/push/names', { names })
export const pushUnsubscribe = () => post('/api/push/unsubscribe', {})
export const pushTest = () => post('/api/push/test', {})

// pair trades a one-time code from `dyna engine run --pair` for this
// browser's device cookie.
export function pair(code, name) {
  return post('/api/pair', { code, name })
}

// logout forgets this browser's device on the server.
export function logout() {
  return post('/api/logout', [])
}

// onEvent subscribes to the live stream: {kind: "posts"|"rooms"|"members"|
// "runs"|"pins", roomId, posts}. It sends {kind: "resync"} when events were
// lost, so the app reloads what it shows. See openStream for resume, the
// long-poll fallback and the silence rule. The second argument runs with
// false when the stream is lost and true when it is back, so the app can
// look for another route.
export function onEvent(fn, state = () => {}) {
  return openStream(fn, state, {
    open: (url) => new EventSource(url),
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
export async function engineCheck() {
  try {
    const r = await authenticatedFetch('/api/Health', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' }, body: '[]', signal: AbortSignal.timeout(4000) })
    return r.status
  } catch {
    return 'network'
  }
}

// copyText uses the browser clipboard.
export async function copyText(text) {
  await navigator.clipboard.writeText(text)
}

export function openExternal(url) {
  window.open(url, '_blank', 'noopener,noreferrer')
}

// fileURL is where the app serves a saved attachment's bytes.
export function fileURL(roomId, id) {
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
const cards = new Map()
export function cardURL(roomId, id) {
  const key = `${roomId}/${id}`
  const hit = cards.get(key)
  if (hit && hit.expires - Date.now() > 60000) return Promise.resolve(hit.url)
  return post('/api/CardURL', [roomId, id]).then((ref) => {
    cards.set(key, { url: ref.url, expires: Date.parse(ref.expires) })
    return ref.url
  })
}

// termAttach sends {id,data,start,offset,reset?,exited,exitCode} to output.
// data is base64 raw PTY bytes. Return the detach function on view unmount;
// detaching never closes the shell. Byte offsets prevent duplicate output.
export async function termAttach(id, output, after = 0) {
  let stopped = false
  let cursor = after
  let controller = null
  let retryTimer = null
  let wakeRetry = () => {}
  const sleep = (ms) => new Promise((resolve) => { wakeRetry = resolve; retryTimer = setTimeout(resolve, ms) })
  const accept = (ev) => {
    if (stopped || ev.id !== id) return
    // An out-of-order event may overlap the initial scrollback.
    if (!ev.reset && ev.offset < cursor) return
    if (!ev.reset && ev.start < cursor) {
      const raw = atob(ev.data || '')
      ev = { ...ev, data: btoa(raw.slice(cursor - ev.start)), start: cursor }
    }
    if (ev.data || ev.exited || ev.reset) output(ev)
    cursor = ev.offset
    if (ev.exited) stopped = true
  }
  const detach = () => {
    stopped = true
    controller?.abort()
    clearTimeout(retryTimer)
    wakeRetry()
  }

  // A stream held by a proxy falls back to bounded snapshot calls. The
  // terminal keeps running while either transport reconnects.
  let polling = false
  const run = async () => {
    let wait = 500
    while (!stopped) {
      let helloTimer = null
      try {
        if (polling) {
          accept(await app.TermAttach(id, cursor))
          if (!stopped) await sleep(250)
          continue
        }
        controller = new AbortController()
        helloTimer = setTimeout(() => { polling = true; controller.abort() }, 6000)
        const response = await authenticatedFetch('/api/terminal/attach', {
          method: 'POST', credentials: 'same-origin', signal: controller.signal,
          headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' },
          body: JSON.stringify({ id, after: cursor }),
        })
        if (!response.ok) {
          const error = await response.json().catch(() => null)
          // Missing terminals and auth refusal are final, rather than a
          // network interruption that should retry forever.
          if (response.status >= 400 && response.status < 500) {
            output({ id, data: '', start: cursor, offset: cursor, exited: true, exitCode: -1, error: error?.error })
            stopped = true
            return
          }
          throw new Error(error?.error || `HTTP ${response.status}`)
        }
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        try {
          while (!stopped) {
            const { value, done } = await reader.read()
            if (done) break
            buffer += decoder.decode(value, { stream: true })
            let end
            while ((end = buffer.indexOf('\n\n')) !== -1) {
              const frame = buffer.slice(0, end)
              buffer = buffer.slice(end + 2)
              if (frame.includes('event: hello')) { clearTimeout(helloTimer); wait = 500 }
              else if (!frame.includes('event: ping')) {
                const line = frame.split('\n').find((s) => s.startsWith('data: '))
                if (line) accept(JSON.parse(line.slice(6)))
              }
            }
          }
        } finally { await reader.cancel().catch(() => {}) }
      } catch (error) {
        // Snapshot calls also need backoff when a polling connection fails.
        if (String(error).includes('no such terminal')) {
          output({ id, data: '', start: cursor, offset: cursor, exited: true, exitCode: -1, error: String(error) })
          stopped = true
        } else if (!stopped && polling) await sleep(1000)
      } finally { clearTimeout(helloTimer) }
      if (!stopped && !polling) { await sleep(wait); wait = Math.min(wait * 2, 5000) }
    }
  }
  run()
  return detach
}
