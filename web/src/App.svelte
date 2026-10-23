<script lang="ts">
  import { refreshHeldComments } from './lib/prs/heldComments.ts'
  import { actions, actionItems, roomMenu as roomActionItems } from './lib/ui/actions.ts'
  import { createReloadGate, listVisible, reconcileRows } from './lib/ui/reloadGate.ts'
  import Skeleton from './lib/ui/Skeleton.svelte'
  import { untrack } from 'svelte'
  import { fade } from 'svelte/transition'
  import { FAST, motion } from './lib/ui/motion.ts'
  import { viewIdentity } from './lib/ui/viewIdentity.ts'
  import { createReadMarker } from './lib/rooms/readMarker.ts'
  import { mergeFeedPage, rerouteAnchors, withChannels } from './lib/rooms/feedPages.ts'
  import { createRoomData } from './lib/rooms/roomData.ts'
  import { createSessionData } from './lib/sessions/sessionData.ts'
  import { mergeListPage } from './lib/ui/listPages.ts'
  import { patchListRows } from './lib/ui/listChanges.ts'
  import { reloadsUnread } from './lib/ui/unreadReload.ts'
  import { createWorkflowData } from './lib/workflows/workflowData.ts'
  import { onMount, tick } from 'svelte'
  import { errorText } from './lib/errorText.ts'
  import { api, onEvent, engineCheck, openExternal, copyText, onUnauthorized, logout } from './api.ts'
  import RoomRail from './lib/rooms/RoomRail.svelte'
  import Sidebar from './lib/rooms/Sidebar.svelte'
  import CallButton from './lib/voice/CallButton.svelte'
  import VoiceWidget from './lib/voice/VoiceWidget.svelte'
  import { voiceCall, callRequests } from './lib/voice/voiceCall.ts'
  import { callLink, callTarget } from './lib/voice/callRequests.ts'
  import { GENERAL, VOICE, isChannel, inChannel, homeChannel, channelModel, parentTrail, prepareChannelText } from './lib/rooms/channels.ts'
  import { isShellResult } from './lib/rooms/feedRows.ts'
  import { HOOK_LOG_EVENT, hookLogFrom } from './lib/settings/hookLog.ts'
  import type { HookLogTarget } from './lib/settings/hookLog.ts'
  import { mergeSessionPage } from './lib/sessions/sessionPages.ts'
  import { trimFeed } from './lib/rooms/feedWindow.ts'
  import { look, revealed, reveal as revealPost } from './lib/rooms/look.svelte.ts'
  import { visibleTo } from './lib/rooms/look.ts'
  import Feed from './lib/rooms/Feed.svelte'
  import Composer from './lib/rooms/Composer.svelte'
  import Members from './lib/rooms/Members.svelte'
  import Toast from './lib/ui/Toast.svelte'
  import { clipText, dismissDelay, makeToast } from './lib/ui/toast.ts'
  import type { Toast as ToastLine, ToastDetail, ToastInput } from './lib/ui/toast.ts'
  import { createRoomSessionControls, roomControlLabels, roomControlSummary } from './lib/rooms/roomSessionControls.ts'
  import type { RoomControlSummary, RoomControlValue } from './lib/rooms/roomSessionControls.ts'
  import { TOAST_DETAIL_MS, TOAST_MS } from './lib/ui/toast.ts'
  import Pins from './lib/rooms/Pins.svelte'
  import SearchIcon from './lib/ui/SearchIcon.svelte'
  import SyncBadge from './lib/rooms/SyncBadge.svelte'
  import { remoteList, syncDelay } from './lib/rooms/sync.ts'
  import Library from './lib/library/Library.svelte'
  import Questions from './lib/rooms/Questions.svelte'
  import Visuals from './lib/rooms/Visuals.svelte'
  import Services from './lib/services/Services.svelte'
  import { questionID, visualKey } from './lib/rooms/questions.ts'
  import { setVisuals } from './lib/files/visuals.svelte.ts'
  import { changesVisuals } from './lib/files/visuals.ts'
  import Goals from './lib/rooms/Goals.svelte'
  import GoalDetail from './lib/rooms/GoalDetail.svelte'
  import { expedite, expediteScope, expediteAction, heldGoals, isExpedited } from './lib/rooms/expedite.ts'
  import { expediteMenuItem } from './lib/rooms/expediteMenu.ts'
  import ExpediteGoal from './lib/rooms/ExpediteGoal.svelte'
  import Rules from './lib/rooms/Rules.svelte'
  import { nextRules } from './lib/rooms/rules.ts'
  import PinIcon from './lib/ui/PinIcon.svelte'
  import { isFinished } from './lib/rooms/memberOrder.ts'
  import Session from './lib/sessions/Session.svelte'
  import DmIdentity from './lib/sessions/DmIdentity.svelte'
  import Home from './lib/rooms/Home.svelte'
  import ProfileModal from './lib/settings/ProfileModal.svelte'
  import Viewer from './lib/files/Viewer.svelte'
  import Menu from './lib/ui/Menu.svelte'
  import Confirm from './lib/ui/Confirm.svelte'
  import BulkSessionAction from './lib/sessions/BulkSessionAction.svelte'
  import { createBulkSessionAction, bulkRetryable, bulkTitles } from './lib/sessions/bulkSessions.ts'
  import SessionConsolidation from './lib/sessions/SessionConsolidation.svelte'
  import { pruneSelection, toggleSelection, selectionKey, selectableSession } from './lib/sessions/consolidation.ts'
  import ModelTransfer from './lib/sessions/ModelTransfer.svelte'
  import Runs from './lib/workflows/Runs.svelte'
  import Run from './lib/workflows/Run.svelte'
  import Settings from './lib/settings/Settings.svelte'
  import Pair from './lib/settings/Pair.svelte'
  import Palette from './lib/ui/Palette.svelte'
  import ConnectStop from './lib/settings/ConnectStop.svelte'
  import ReviewWorkspace from './lib/review/ReviewWorkspace.svelte'
  import ReviewIcon from './lib/ui/ReviewIcon.svelte'
  import SessionTabs from './lib/sessions/SessionTabs.svelte'
  import UseIcons from './lib/sessions/UseIcons.svelte'
  import ShellGrid from './lib/sessions/ShellGrid.svelte'
  import { runningShells } from './lib/sessions/shells.ts'
  import Tabs from './lib/ui/Tabs.svelte'
  import { rowOf, visibleTabs } from './lib/rooms/workViews.ts'
  import SessionMedia from './lib/sessions/SessionMedia.svelte'
  import ComputerPane from './lib/computers/ComputerPane.svelte'
  import Computers from './lib/computers/Computers.svelte'
  import ComputerPopout from './lib/computers/ComputerPopout.svelte'
  import ComputerPreview from './lib/computers/ComputerPreview.svelte'
  import { previewVisible, inUse } from './lib/computers/preview.ts'
  import { applyEvent, pushAction, openedApps, popOut } from './lib/computers/computers.ts'
  import Terminal from './lib/sessions/Terminal.svelte'
  import { available as hasTerminal } from './lib/sessions/TermPane.svelte'
  import Jobs from './lib/workflows/Jobs.svelte'
  import Prompt from './lib/ui/Prompt.svelte'
  import RunWorkflow from './lib/workflows/RunWorkflow.svelte'
  import GitHub from './lib/prs/GitHub.svelte'
  import type { Reference } from './lib/rooms/references.ts'
  import type { RoomIssue } from './lib/prs/unifiedIssues.ts'
  import RoomSettings from './lib/rooms/RoomSettings.svelte'
  import { prsBySession, isOpen, withoutReviewers } from './lib/prs/pr.ts'
  import { boot, watch, registerWorker } from './lib/settings/link.ts'
  import { engineWatch, browserEngine } from './lib/engineStatus.ts'
  import { loadLayout, saveLayout, lastRoom, saveLastRoom } from './lib/ui/prefs.ts'
  import { dmView } from './lib/sessions/dm.ts'
  import { roomLabel } from './lib/ui/format.ts'
  import { parseDetailHref } from './lib/ui/details.ts'
  import { prHash, parsePRHash, parseIssueHash, parseFileHash } from './lib/prs/prlink.ts'
  import { ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute } from './lib/ui/route.ts'
  import { paletteTargets } from './lib/ui/paletteTargets.ts'
  import MachinePage from './lib/machines/MachinePage.svelte'
  import { machinesSub } from './lib/machines/machines.ts'
  import { LOAD_POLL_MS } from './lib/rooms/systemLoad.ts'
  import { roomIDPattern as ROOM, postIDPattern as POST } from './lib/ui/posts.ts'
  import type { MachineDetail, MachinePatch, MachineSummary } from './lib/api/machine.ts'
  import type { Room, Member, Post, Feed as FeedPage, Question, Visual, ProfileChoice, Attachment } from './lib/api/room.ts'
  import type { AttachmentRef } from './lib/api/engine.ts'
  import type { Session as SessionData, QueueItem, MemberProfile } from './lib/api/session.ts'
  import type { Goal } from './lib/api/goal.ts'
  import type { RunList, RunDetail, RunSummary } from './lib/api/workflow.ts'
  import type { ShellJob } from './lib/api/terminal.ts'
  import type { ComputerInfo, ComputerEvent } from './lib/api/computer.ts'
  import type { StreamEvent, ListChange } from './lib/api/event.ts'
  import type { UpdateNotice } from './lib/api/settings.ts'
  import type { RowChange } from './lib/ui/listChanges.ts'
  import type { Page } from './lib/api/wire.ts'
  import type { ChannelMember } from './lib/rooms/channels.ts'
  import type { Receipt } from './lib/sessions/dm.ts'
  import type { ActionRoom } from './lib/ui/actions.ts'
  import type { Target as PaletteTarget } from './lib/ui/paletteTargets.ts'
  import type { ReviewerStatus } from './lib/api/change.ts'
  import type { Row as ChangeRow } from './lib/prs/prlist.ts'
  import type { SyncStatus } from './lib/api/lanesync.ts'
  import type { CallRequest } from './lib/voice/callRequests.ts'
  import type { BulkAction, BulkValue } from './lib/sessions/bulkSessions.ts'
  import type { BootResult, RouteNotice } from './lib/settings/link.ts'
  import type { Route } from './lib/ui/route.ts'
  import type { ModelSelection } from './lib/settings/profiles/state.ts'
  import type { RunAgent } from './lib/api/workflow.ts'

  // A member the open view drills into. The root conversation is one made from the seat.
  // AgentRef is the part of a session that opening its view needs. The member
  // list gives a Member and a machine's usage rows give a load owner.
  interface AgentRef {
    id?: string
    name: string
    hue: number
    address?: string
    room?: string
    roomId?: string
  }
  interface Drill extends Member {
    id: string
    rootSeat?: boolean
  }
  interface PromptSpec {
    title: string
    lines?: string[]
    label?: string
    value?: string
    multiline?: boolean
    placeholder?: string
    match?: string
    action?: string
    danger?: boolean
    submit: (value: string) => void | Promise<unknown>
  }
  interface RoomContext {
    roomId: string
    visit: number
  }
  interface SelectionContext {
    roomId: string
    ids: string[]
    key: string
  }
  interface MenuItem {
    label: string
    run: () => unknown
    hint?: string
    danger?: boolean
    gap?: boolean
    disabled?: boolean
  }
  interface RuleList {
    hash: string
  }
  interface RuleSet {
    room: RuleList
    global: RuleList
    hash: string
  }
  interface BulkRecord {
    key: number
    roomId: string
    roomRequest: number
    selectionRevision: number
    sessions: (Member & { id: string })[]
    action: BulkAction
    value?: BulkValue
  }
  type Confirming =
    | { kind: 'promote' | 'stop' | 'close'; m: Member; busy?: boolean; error?: string }
    | { kind: 'forceclose'; m: Member; items: string[]; busy?: boolean; error?: string }
    | { kind: 'cancelrun'; run: Pick<RunSummary, 'id' | 'name' | 'status'>; busy?: boolean; error?: string }
    | { kind: 'expedite'; goal: Goal; roomId: string; busy?: boolean; error?: string }
  // A page of one list, with its continuation cursor.
  interface ListPage<T> {
    items: T[] | null
    older?: boolean
    cursor?: string
    limit?: number
    loadedOlder?: boolean
  }
  interface ListKinds {
    goals: Goal
    changes: ChangeRow
    jobs: ShellJob
  }
  type ListKind = keyof ListKinds
  type ListPages = { [K in ListKind]?: ListPage<ListKinds[K]> }
  // What api.openRoom returns for the open room. The host sends null for an
  // empty list, and the composer's choices are member names.
  interface Opened {
    feed: FeedPage
    members?: Member[] | null
    choices?: string[] | null
    pins?: Post[] | null
    runs?: RunList
  }
  interface EarlierLoad {
    id: string
    roomLoad: number
    direction: string
    promise: Promise<boolean> | null
  }
  interface DetailTarget {
    kind: string
    id: string
    roomId?: string
    turn?: number
  }
  let rooms = $state<Room[]>([])
  // started turns on once the first room is chosen. Before that, the
  // empty room is not the owner's choice and must not overwrite the saved one.
  let started = $state(false)
  let roomId = $state('')
  let voiceWidget = $state<ReturnType<typeof VoiceWidget>>()
  let callPush = $state<ReturnType<typeof callLink>>(null) // the call request a push tap opened
  let roomCalls = $derived($callRequests.filter((r) => r.room === roomId))
  // The room's whole history of call requests, for its feed rows.
  $effect(() => {
    if (roomId) callRequests.load(roomId).catch(() => {})
  })
  const acceptCall = (req: CallRequest) => voiceWidget?.accept(req)
  const declineCall = (req: CallRequest) => voiceWidget?.decline(req)
  let pageVisible = $state(document.visibilityState === 'visible')
  // machinesNav: the rail's machines tile is open, so the sidebar lists the
  // machines of this engine. Picking one opens its page in the main area. A
  // room in the link opens that room instead, so the list starts closed then.
  let machinesNav = $state(!new URLSearchParams(location.hash.slice(1)).has('room'))
  let machines = $state<MachineSummary[] | null>(null) // null until the first answer
  let machinesRequest = 0
  let machineId = $state('') // the machine whose page is open, or ''
  let machineDetail = $state<MachineDetail | null>(null)
  let machineError = $state('')
  let machineBusy = $state(false)
  let machineProbing = $state(false)
  let machineRequest = 0
  function machinesVisible() { return pageVisible && machinesNav && (narrow ? navOpen : showSide) }
  function machineVisible() { return pageVisible && machineId !== '' }
  // The figures move on their own, so the list and the open page read again at
  // the load card's cadence. A phone keeps the page while the drawer is shut,
  // so the page reads on its own terms, not the sidebar's.
  $effect(() => {
    const list = machinesVisible()
    const page = machineVisible()
    if (!list && !page) return
    const tick = () => {
      if (list) loadMachines()
      if (page && !machineBusy && !machineProbing) loadMachine(machineId)
    }
    tick()
    const timer = setInterval(tick, LOAD_POLL_MS)
    return () => clearInterval(timer)
  })
  let home = $state<ReturnType<typeof Home>>() // the home screen, which takes live events
  // loadMachines reads the machine list now. A slow answer never replaces a
  // newer one.
  function loadMachines() {
    const request = ++machinesRequest
    return api.machines().then((list) => { if (request === machinesRequest) machines = list }, () => {})
  }
  // loadMachine reads the open machine's page. A slow answer never replaces a
  // newer one, and an answer for a machine that closed is dropped.
  function loadMachine(id: string) {
    const request = ++machineRequest
    if (machineId !== id) machineDetail = null
    return api.machine(id).then((detail) => {
      if (request === machineRequest && machineId === id) machineDetail = detail
    }, (e) => {
      if (request === machineRequest && machineId === id) machineError = errorText(e)
    })
  }
  // openMachine shows one machine's page in the main area. On a phone the tap
  // closes the drawer, as every other view does.
  function openMachine(id: string) {
    machineId = id
    navOpen = false
    machineError = ''
    machineBusy = false
    loadMachine(id)
  }
  function closeMachine() {
    machineId = ''
    machineDetail = null
    machineError = ''
    if (narrow) navOpen = true
  }
  // probeResult words what one probe found as a typed toast. A degraded machine
  // answered, so it never reads as unreachable: it holds its CI work instead.
  function probeResult(detail: MachineDetail): ToastInput {
    if (detail.status === 'degraded') {
      if (detail.statusReason === 'low-disk') {
        return { tone: 'error', subject: `${detail.name} is low on disk`, text: 'Dyna starts no CI work here until space is freed.' }
      }
      return { tone: 'error', subject: `${detail.name} needs attention`, text: 'Open the machine page to see why.' }
    }
    if (detail.status === 'offline' || detail.status === 'unknown') {
      return { tone: 'error', subject: `${detail.name} did not answer the probe`, text: 'Check the SSH route, then probe again.' }
    }
    return { tone: 'success', subject: `Probed ${detail.name}` }
  }
  // saveMachine writes one setting and shows the machine's next state. A
  // refused change keeps the old values and shows the engine's reason.
  async function saveMachine(patch: MachinePatch) {
    const id = machineId
    if (!id || machineBusy) return
    machineBusy = true
    machineError = ''
    // A read that started before this write may not land after it.
    machineRequest++
    try {
      const detail = await api.saveMachine(id, patch)
      if (machineId === id) machineDetail = detail
      loadMachines()
    } catch (e) {
      if (machineId === id) machineError = errorText(e)
    } finally {
      machineBusy = false
    }
  }
  // probeMachine reads the open machine now and shows its next health. A probe
  // that could not reach the machine says so in the page's status; the toast
  // words the result in one line and never repeats the engine's error text.
  async function probeMachine() {
    const id = machineId
    if (!id || machineBusy || machineProbing) return
    machineProbing = true
    machineError = ''
    // A read that started before this probe may not land after it.
    machineRequest++
    const name = machineDetail?.name ?? 'The machine'
    try {
      const detail = await api.probeMachine(id)
      if (machineId === id) machineDetail = detail
      loadMachines()
      say(probeResult(detail))
    } catch {
      if (machineId === id) say({ tone: 'error', subject: `Could not probe ${name}` })
    } finally {
      machineProbing = false
    }
  }
  // SavedFeed is the feed the page keeps: the server's page plus the client's history flag.
  type SavedFeed = FeedPage & { historyLoaded?: boolean }
  let feed = $state<SavedFeed | null>(null)
  let room = $derived(feed?.room ?? null)
  let feedAtBottom = $state(true)
  let members = $state<Member[]>([])
  let hierarchy = $state<Member[]>([])
  let choices = $state<string[]>([])
  let view = $state<string>(GENERAL)
  let prLink = $state<ReturnType<typeof parsePRHash> | ReturnType<typeof parseIssueHash>>(null) // a PR link from the address bar, for the Pull requests view
  let unreadFrom = $state('')
  let drill = $state<Drill | null>(null) // the member whose transcript is open
  let readOnlyDm = $state(false) // the open transcript came from Services: no header tabs, no composer
  let session = $state<SessionData | null>(null)
  let sessionReadingHistory = $state(false)
  let threadCount = $derived(dmView(session, room || { name: '' }).threads.length)
  let sessionTurn = $state(0) // a completion link opens its exact stored turn
  let openJob = $state('')
  let sessionVisit = 0
  let detailRequest = 0
  let roomRequest = 0 // room data can still load after a later view choice
  let queue = $state<QueueItem[]>([]) // steering that waits for the open session's turn
  let sessionError = $state('')
  const layout = loadLayout({ side: true, members: true, threads: true })
  let threadsOpen = $state(layout.threads)
  let selectedThread = $state('')
  let listPages = $state<ListPages>({})
  let olderLoading = $state(false)
  let olderFailed = $state(false) // the last older-page load failed
  let jobs = $state<ShellJob[] | null>(null) // the room's shell jobs, newest first: one list for every view
  let jobsError = $state('')
  $effect(() => saveLayout({ threads: threadsOpen }))
  let showSide = $state(layout.side) // the room sidebar next to the rail
  // A narrow screen (a phone) has one column: the rail and the sidebar
  // become a drawer, and the members or pins panel a sheet over the feed.
  // The desktop window never gets this narrow (its minimum is 960 px).
  const NARROW = '(max-width: 760px)'
  let narrow = $state(window.matchMedia(NARROW).matches)
  let navOpen = $state(false) // the drawer, on a narrow screen
  let showMembers = $state(layout.members && !window.matchMedia(NARROW).matches)
  // The saved layout is the wide one; a phone's toggles do not change it.
  $effect(() => { if (!narrow) saveLayout({ side: showSide, members: showMembers }) })
  // The open room and its feed view come back after a restart.
  $effect(() => {
    const id = roomId
    const v = view
    if (started) saveLastRoom(id, v)
  })
  // The address bar follows the open screen; see syncRoute.
  let routing = $state(0) // links being followed
  let routeFix = true
  $effect(() => syncRoute())
  let needPair = $state(new URLSearchParams(location.hash.slice(1)).has('pair')) // a pairing link or a server 401 opens the pairing screen
  let halt = $state<BootResult | null>(null) // {key} or {error}: Connect stopped the app before it started
  let route = $state<RouteNotice | null>(null) // the route this page uses, for the sidebar
  let showClosed = $state(false)
  let showPins = $state(false) // the Pinned panel takes the member list's place
  let questions = $state<Question[]>([])
  let visuals = $state<Visual[]>([])
  let questionFocus = $state('')
  let questionFocusRequest = $state(0)
  let questionsRequest = 0
  let pins = $state<Post[]>([]) // the room's pinned posts, newest pin first
  let syncStatus = $state<SyncStatus>({}) // saved remote counts by repo path
  let syncRepo = $derived(room?.repo ? syncStatus[room.repo] ?? null : null) // {remotes, integration, operation?}
  let syncRemotes = $derived(remoteList(syncRepo?.remotes))
  let syncNote = $state('') // the last Refresh or Export answer that was not a start
  let issues = $state<RoomIssue[]>([]) // open issues, newest first
  let issueHistory = $state<RoomIssue[]>([]) // closed and converted issues, newest first
  let feedView = $state<ReturnType<typeof Feed>>()
  let pinnedIds = $derived(new Set(pins.map((p) => p.id)))
  let reply = $state<Post | null>(null)
  let drafts = $state<Record<string, string>>({})
  let dataDir = $state('')
  let loadError = $state('')
  let health = $state('')
  let engineStatus = $state('') // why the event stream is down, see engineWatch
  let composer = $state<ReturnType<typeof Composer>>()
  let card = $state<Drill | null>(null) // the member whose profile modal is open
  let profile = $state<MemberProfile | null>(null)
  let profileError = $state('')
  let sessionView = $state<ReturnType<typeof Session>>()
  let viewing = $state<(Attachment & { roomId?: string; postId?: string }) | null>(null) // the attachment open in the viewer; roomId names its room when it is not this one (a call's shared file)
  let dropping = $state(false) // files are being dragged over the window
  let profiles = $state<ProfileChoice[]>([]) // what "/" completes in the composer
  let fresh = $state('') // a session just started from the composer: its row flashes
  let menu = $state<{ m: Member; x: number; y: number; anchor?: HTMLElement } | null>(null) // {m, x, y, anchor}: the open session menu
  let roomMenu = $state<{ r: Room; x: number; y: number; anchor?: HTMLElement } | null>(null) // {r, x, y, anchor}: the open room menu
  let goals = $state<Goal[]>([])
  let goalId = $state('')
  // The room's one expedited goal, kept for the badges. A timer wakes it at the
  // deadline, so the badges leave on time without a server event.
  let expireTick = $state(Date.now())
  let fetchedGoal = $state<Goal | null>(null)
  let knownGoals = $derived.by(() => {
    const fetched = fetchedGoal
    return fetched && !goals.some((g) => g.id === fetched.id) ? [...goals, fetched] : goals
  })
  // Items carry the expedite scope's assignees. They load for each room's
  // expedited goal, room:goal -> {at, items}, and load again when the goal
  // changes, so a new assignee gets the badge without a reload.
  let expediteGoalItems = $state<Record<string, { at: string; items: { state?: string; assignee?: string }[] }>>({})
  // withItems attaches the loaded items to the expedited goal, so the scope
  // can cover item assignees.
  function withItems(room: string, list: Goal[]) {
    const g = list.find((x) => isExpedited(x))
    const items = g && expediteGoalItems[room + ':' + g.id]?.items
    return g && items ? list.map((x) => (x.id === g.id ? { ...x, items } : x)) : list
  }
  $effect(() => {
    const wanted = []
    const here = knownGoals.find((x) => isExpedited(x))
    if (here) wanted.push({ room: roomId, goal: here })
    for (const { room, goal } of wanted) {
      const key = room + ':' + goal.id
      const cached = untrack(() => expediteGoalItems[key])
      if (cached && cached.at === goal.updatedAt) continue
      api.goalItems(room, goal.id).then((items: { state?: string; assignee?: string }[] | null) => { expediteGoalItems = { ...expediteGoalItems, [key]: { at: goal.updatedAt, items: items ?? [] } } }, () => {})
    }
  })
  $effect(() => {
    const now = Math.max(expireTick, Date.now())
    const scope = expediteScope(withItems(roomId, knownGoals), hierarchy, now)
    expedite.set({ ...scope, agents: new Set(scope.agents) })
    const until = scope.goalId ? scope.until : Number.POSITIVE_INFINITY
    if (!Number.isFinite(until)) return
    const timer = setTimeout(() => (expireTick = Date.now()), Math.min(until - Date.now() + 50, 2 ** 31 - 1))
    return () => clearTimeout(timer)
  })
  // A goal opened by a link but held by no loaded page; list refreshes keep it.
  // Counts change events applied to fetchedGoal, so a slower reload answer
  // never replaces a newer event.
  let fetchedGoalVersion = 0
  let selectedGoal = $derived(goalById(goalId))
  // rules is the list in the Room settings Rules section; rulesPending waits while an inline edit
  // is open (rulesEditing) and replaces it when the edit closes.
  let rules = $state<RuleSet | null>(null)
  let rulesPending = $state<RuleSet | null>(null)
  let rulesEditing = $state(false)
  $effect(() => { if (!rulesEditing && rulesPending) { rules = rulesPending; rulesPending = null } })
  let reviewer = $state<ReviewerStatus | null>(null) // the room's reviewer (ReviewerStatus)
  let changes = $state<ChangeRow[] | null>(null) // the room's changes, local and PR (Changes); null before they load or on a host without the API
  let mergeVersion = $state(0)
  // The room's pull requests: the Changes rows that are not local changes.
  let prs = $derived((changes ?? []).filter((c) => c.kind !== 'local'))
  let prError = $state('')
  let prMap = $derived(prsBySession(prs))
  // Agent rows show a chip only while a PR is open; the panel lists them all.
  let openPRMap = $derived(prsBySession(prs.filter(isOpen)))
  let confirm = $state<Confirming | null>(null)
  const bulkControllers = new Map<number, ReturnType<typeof createBulkSessionAction>>()
  let bulkSerial = 0
  let activeBulk = $state(0)
  let bulkBatches = $state<BulkRecord[]>([])
  let selectionRevision = 0
  let currentBulk = $derived(bulkBatches.find((b) => b.key === activeBulk))
  let roomSessionController: ReturnType<typeof createRoomSessionControls> | null = null
  let roomControls = $state<RoomControlValue | null>(null)
  let controlsReported = ''
  let roomControlsMenu = $state<(RoomContext & { x: number; y: number; anchor?: HTMLElement }) | null>(null)
  let clearDormantConfirm = $state<(RoomContext & { count: number }) | null>(null)
  let roomControlsVisit = 0
  let selectionMenu = $state<(SelectionContext & { x: number; y: number; anchor?: HTMLElement }) | null>(null)
  let selectingSessions = $state(false)
  let pickedSessions = $state<string[]>([])
  let consolidating = $state<{ roomId: string; sessions: Member[]; key: number } | null>(null)
  let consolidationView = 0
  let consolidationAlive = true
  let transferring = $state<Member | null>(null) // {kind: 'stop'|'close', m, busy, error}
  let expediting = $state<{ member: Member; goals: Goal[] } | null>(null) // the goal picker for a session with several goals
  let toast = $state<ToastLine | null>(null)
  let toastDetail = $state<ToastDetail | null>(null)
  let toastDetailOpen = $state(false)
  let closeResult = $state<{ name: string; items: string[] } | null>(null)
  let runs = $state<RunList | null>(null) // roomsvc.RunList for the room
  let runsError = $state('')
  let openRun = $state('') // the run whose detail is open, in the runs view
  let runDetail = $state<RunDetail | null>(null)
  let runError = $state('')
  let showSettings = $state(false)
  let showRoomSettings = $state(false)
  let settingsKey = $state(0)
  let roomSettingsKey = $state(0)
  let palettePath = $state<string | null>(null) // set: the palette opens on New room with this path
  let settingsAt = $state<{ start: string; pairNow: boolean; profile?: string; accountId?: string; initialSelection?: ModelSelection }>({ start: 'profiles', pairNow: false }) // where Settings opens
  let roomSettingsAt = $state<{ start: string; hookRun?: HookLogTarget | null }>({ start: 'general' }) // where Room settings opens
  let showPalette = $state(false) // Ctrl+K, or the search button on a phone
  let update = $state<UpdateNotice | null>(null) // configsvc.UpdateNotice: a newer dyna release
  let updateHidden = $state(false)
  let sessionTab = $state('chat') // the open tab of a session view
  let reviewOpen = $state(false)
  let reviewTab = $state('files')
  let reviewVersion = $state(0)
  let prView = $derived(view === 'issues')
  // Agent computers of the open room (null when the feature is unavailable),
  // each session's recent actions, and whether the viewer is open.
  let computers = $state<ComputerInfo[] | null>(null)
  let computerFeed = $state<ReturnType<typeof pushAction>>({})
  let computerError = $state('')
  let winWidth = $state(typeof window === 'undefined' ? 1400 : window.innerWidth)
  const popMatch = /^#pop=(room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}):([A-Za-z0-9][A-Za-z0-9_.-]{0,95})$/.exec(typeof location === 'undefined' ? '' : location.hash)
  const pop = popMatch ? { room: popMatch[1], session: popMatch[2] } : null
  let computerRequest = 0
  function loadComputers() { return reload('computers', async (current: () => boolean) => {
    if (!roomId) return
    const id = roomId, request = ++computerRequest
    try {
      const list = await api.computers(id)
      if (!current() || id !== roomId || request !== computerRequest) return
      computers = reconcileRows(computers ?? [], list ?? [], 'session')
      computerError = ''
    } catch (e) {
      if (!current() || id !== roomId || request !== computerRequest) return
      computerError = String(e)
    }
  }) }
  let computerTimer: ReturnType<typeof setTimeout> | undefined
  function handleComputer(ev: ComputerEvent) {
    if (ev.action) computerFeed = pushAction(computerFeed, ev.session, ev.action)
    if (!computers) return
    // An app that starts reads "Opened Chromium" in the pill.
    const before = computers.find((c) => c.session === ev.session)
    for (const name of ev.computer ? openedApps(before?.apps, ev.computer.apps) : []) computerFeed = pushAction(computerFeed, ev.session, `open · ${name}`)
    const r = applyEvent(computers, ev)
    computers = r.list
    if (r.needsReload) {
      clearTimeout(computerTimer)
      computerTimer = setTimeout(loadComputers, 250)
    }
  }
  // The floating preview and the phone chip. The preview shows in a
  // session's Messages view only, never opens a stream, and opens the Computer tab on a click.
  const drillComputer = $derived(drill ? computerOf(drill.id) : null)
  // view is '': a session view is open, so the computers page is not.
  const showPreview = $derived(!!drill && !!drillComputer && previewVisible({ c: drillComputer, tab: sessionTab, view: '', width: winWidth, hidden: null }))
  const drillShells = $derived(drill ? runningShells(jobs, drill) : [])
  function computerOf(id?: string) { return computers?.find((c) => c.session === id) ?? null }
  let reveal = $state('') // a file the Files view opens, from Changes
  let revealFolder = $state<{ path: string } | null>(null) // a folder the Files view shows, from Library; an object so the root and repeats still open
  let starting = $state(false) // the Run workflow dialog is open
  let ask = $state<PromptSpec | null>(null) // {title, lines, label, value, multiline, match, action, danger, submit}: the open Prompt
  // The sidebar row of the open work view, and its tabs when it has several.
  let workRow = $derived(rowOf(view))
  let workTabs = $derived(visibleTabs(workRow))
  // The room feed is on screen: a channel view, not a work view or a session.
  let feedShown = $derived(!!room && !drill && isChannel(view) && !workRow)

  function hasFiles(e: DragEvent) {
    return Array.from(e.dataTransfer?.types || []).includes('Files')
  }
  function dragOver(e: DragEvent) {
    if (!hasFiles(e) || !room) return
    e.preventDefault()
    dropping = true
  }
  function drop(e: DragEvent) {
    dropping = false
    if (!hasFiles(e)) return
    e.preventDefault()
    const files = e.dataTransfer?.files ?? []
    if (drill) sessionView?.addFiles(files)
    else composer?.addFiles(files)
  }

  let current = $derived(rooms.find((r) => r.id === roomId))
  let channels = $derived(channelModel(hierarchy))
  let channel = $derived(view.startsWith('channel:') ? channels.byId.get(view.slice(8)) : null)
  // A phone hides the older crumbs, so the current one carries the whole trail.
  // The root DM is a session view like any other; it keeps the room's voice call button.
  let isRootDm = $derived(!!drill && (drill.rootSeat || drill.role === 'root'))
  let rootSeat = $derived(hierarchy.find((m) => m.owner)?.seat ?? {})
  // The root conversation opens like any session. A root with no session of its own (an external
  // seat holder, or an internal root that has not started) is a member made from the seat; its
  // posts load through RootSession.
  function rootMember(): Drill {
    const owner = hierarchy.find((m) => m.owner)
    const holder = rootSeat.external ? (rootSeat.harness ? rootSeat.harness + ' (external)' : 'External orchestrator') : 'Internal root'
    return { id: 'root:' + roomId, rootSeat: true, role: 'root', name: 'root', address: room?.name + ':root', hue: owner?.hue ?? 0, state: 'idle', status: 'seat', profile: holder + ' holds the seat', seat: rootSeat, resumable: false, depth: 0, children: 0, turns: 0, created: '', lastActivity: '' }
  }
  // The breadcrumbs above a session and a run: root, then the parent sessions.
  let sessionTrail = $derived(drill && !readOnlyDm && !isRootDm ? parentTrail(channels, drill.parent, rootMember()).filter((m) => m.id !== drill?.id) : [])
  let runTrail = $derived(view === 'runs' && openRun && runDetail?.run?.parentId ? parentTrail(channels, runDetail.run.parentId, rootMember()) : [])
  let channelLabel = $derived(channel?.name ?? roomLabel(current ?? room))
  // Shell exit records live in shell-jobs, not in channel feeds.
  let posts = $derived((feed?.posts ?? []).filter((p) => !isShellResult(p) && (!isChannel(view) || inChannel(p, view))))
  // What the owner chose to show in this channel; hidden posts still count as read.
  let shownPosts = $derived(isChannel(view) ? posts.filter(visibleTo(look, revealed) as (post: Post) => boolean) : posts)
  // The room's reviewers show only on the PR screen, so every other list uses people.
  let people = $derived(withoutReviewers(members, reviewer))
  let live = $derived(people.filter((m) => !m.owner && m.state === 'live'))
  let ownerHue = $derived(members.find((m) => m.owner)?.hue ?? 0)
  let draft = {
    get value() { return drafts[roomId + ':' + view] ?? '' },
    set value(v) { drafts = { ...drafts, [roomId + ':' + view]: v } },
  }
  // Session and computer chat drafts live here, so a tab switch or a hidden
  // page keeps them. Keys carry the room and the session.
  const sessionDraft = {
    get value() { return drafts['session:' + roomId + ':' + drill?.id] ?? '' },
    set value(v) { drafts = { ...drafts, ['session:' + roomId + ':' + drill?.id]: v } },
  }
  const computerDraft = (v: string) => {
    const key = 'computer:' + roomId + ':' + drill?.id
    if ((drafts[key] ?? '') !== v) drafts = { ...drafts, [key]: v }
  }

  let memberChips = $derived(showMembers && !showPins && !drill)
  let sidebarChips = $derived((narrow ? navOpen : showSide) && (machinesNav || live.length > 0))
  function visibleList(name: string) {
    return consolidationAlive && pageVisible && listVisible(name, {
      view, drill, reviewOpen, showRoomSettings, memberChips, sidebarChips,
      sideVisible: narrow ? navOpen : showSide,
      controlsOpen: !!roomControlsMenu || !!clearDormantConfirm,
    })
  }
  const reload = createReloadGate(() => ({ room: roomId, visit: roomRequest }), visibleList)
  const readMarker = createReadMarker(api, {
    current: () => ({ room: roomId, visit: roomRequest + ':' + view, visible: pageVisible && !(narrow && reviewOpen) && consolidationAlive && !drill && isChannel(view) }),
    read: () => { soonRooms(); soonRooms('feed') },
  })

  // The unread refresh carries channel counts into the open feed.
  function applyUnread(value: { room: Room; channelUnread?: Record<string, number> | null }) {
    if (feed?.room.id !== value.room.id) return
    feed.channelUnread = value.channelUnread ?? {}
  }
  const roomData = createRoomData(api, {
    current: () => ({ room: roomId, visit: roomRequest }),
    visible: visibleList,
    choices: (value: string[]) => { choices = value },
    rooms: (value: Room[]) => { rooms = value; loadError = '' },
    unread: applyUnread,
    members: applyMembers,
    patchMembers: applyMemberItem,
    opened: (value: Opened, initial: boolean) => {
      if (initial) unreadFrom = value.feed.firstUnread ?? (value.feed.posts ?? []).find((p) => p.unread)?.id ?? ''
      applyOpened(value)
    },
    error: (error: string) => (loadError = error),
  })
  function loadRooms() { return roomData.loadRooms() }
  function loadMembers() { return roomData.loadMembers() }
  function memberRoster(list: Member[]) {
    return JSON.stringify(list.filter((m) => !m.owner).map((m) => [m.id, m.name, !!m.closed]).sort())
  }
  // Channels come from the server with each post, so a parent change (a
  // promote) rereads the routing of every loaded post. reconcileRows updates
  // rows in place, so the last key is kept, not recomputed.
  let routedParents = ''
  function parentKey(list: Member[]) {
    return JSON.stringify([roomId, list.filter((m) => !m.owner).map((m) => [m.id, m.parent || '']).sort()])
  }
  // noteParents records the member parent tree. When it changed in the same
  // room, loaded posts reread their channels. Live member updates and room
  // opens, including a reconnect, both come through here.
  function noteParents(rows: Member[]) {
    const parents = parentKey(rows)
    const moved = routedParents !== parents && JSON.parse(routedParents || '[null]')[0] === roomId
    routedParents = parents
    if (moved) rerouteLoaded()
  }
  async function rerouteLoaded() {
    const id = roomId, request = roomRequest
    if (!feed?.posts?.length) return
    try {
      const pages = await Promise.all(rerouteAnchors(feed.posts).map((post) => api.feedAround(id, post, 200)))
      if (id === roomId && request === roomRequest && feed) feed.posts = withChannels(feed.posts ?? [], pages)
    } catch { /* The next room open or reconnect reroutes the posts. */ }
  }
  function roomCountKey(list: Member[]) {
    return JSON.stringify(list.filter((m) => !m.owner && !m.closed).map((m) => [m.id, m.state === 'live']).sort())
  }
  function applyMembers(value: Member[]) {
    const changed = memberRoster(hierarchy) !== memberRoster(value)
    const counts = roomCountKey(hierarchy)
    showMemberRows(reconcileRows(hierarchy, value), changed, counts)
  }
  function applyMemberItem(change: ListChange) {
    const before = memberRoster(hierarchy)
    const counts = roomCountKey(hierarchy)
    const known = hierarchy.some((m) => (m.id || m.address) === change.id)
    const rows = patchListRows(hierarchy, change as RowChange<Member>, (member) => member.id || member.address)
    showMemberRows(rows, before !== memberRoster(rows), known ? counts : null)
    return hierarchy
  }
  function showMemberRows(rows: Member[], rosterChanged: boolean, previousCounts: string | null) {
    noteParents(rows)
    hierarchy = rows
    setPicked(pruneSelection(pickedSessions, hierarchy))
    members = hierarchy.filter((m) => showClosed || !m.closed)
    view = channels.resolveView(view)
    if (rosterChanged) loadComputers()
    if (previousCounts !== roomCountKey(rows)) soonRooms()
  }

  function visitRoomControls(id: string) {
    roomSessionController?.destroy()
    roomSessionController = null
    roomControls = null
    roomControlsMenu = null
    clearDormantConfirm = null
    const visit = ++roomControlsVisit, request = roomRequest
    if (!id) return
    roomSessionController = createRoomSessionControls(api, id, (value: NonNullable<typeof roomControls>) => {
      if (!consolidationAlive || id !== roomId || request !== roomRequest || visit !== roomControlsVisit) return
      const finished = roomControls?.busy && !value.busy
      roomControls = value
      if (finished) { loadMembers(); loadChanges() }
    }, () => visibleList('roomControls'))
  }
  function roomSessionActions(x: number, y: number, anchor?: HTMLElement) {
    roomControlsMenu = { x, y, anchor, roomId, visit: roomControlsVisit }
  }
  // A finished room control reports once, as one short line. The per-session
  // list stays behind Details, so it never pushes the member rows out of view.
  $effect(() => {
    const value = roomControls
    if (!value) return
    if (value.busy) { controlsReported = ''; return }
    if (value.loading || !value.action) return
    const signature = `${roomControlsVisit}|${value.action}|${value.error}|${value.results.map((r) => `${r.session}:${r.ok}:${r.reason ?? ''}`).join(',')}`
    if (signature === controlsReported) return
    controlsReported = signature
    if (value.error) { say({ tone: 'error', subject: `${roomControlLabels[value.action] ?? 'The action'} failed`, text: value.error }); return }
    sayDetails(roomControlSummary(value, hierarchy))
  })
  function closeRoomSessionMenu() { const anchor = roomControlsMenu?.anchor; roomControlsMenu = null; anchor?.focus?.() }
  function currentRoomControls(context: RoomContext | null | undefined) { return consolidationAlive && context?.roomId === roomId && context.visit === roomControlsVisit }
  function askClearDormant(count?: number) {
    if (!consolidationAlive || !roomId || roomControls?.busy) return
    const n = count || roomControls?.state?.dormant || 0
    if (!n) return
    clearDormantConfirm = { roomId, visit: roomControlsVisit, count: n }
  }
  function roomSessionItems(context: RoomContext) {
    const busy = !roomControls?.state || roomControls.busy
    const dormant = roomControls?.state?.dormant || 0
    return [
      { label: 'Pause all', hint: 'let running turns finish', disabled: busy || roomControls?.state?.paused, run: () => { if (currentRoomControls(context)) roomSessionController?.run('pause') } },
      { label: 'Resume all', disabled: busy, run: () => { if (currentRoomControls(context)) roomSessionController?.run('resume') } },
      { label: 'Clear dormant sessions…', gap: true, disabled: busy || !dormant, run: () => { if (currentRoomControls(context)) askClearDormant(dormant) } },
    ]
  }
  function clearRoomDormant(context: RoomContext) {
    if (!currentRoomControls(context) || clearDormantConfirm?.visit !== context.visit) return
    clearDormantConfirm = null
    roomSessionController?.run('clearDormant')
  }

  function setPicked(ids: string[]) {
    if (JSON.stringify(ids) !== JSON.stringify(pickedSessions)) { pickedSessions = ids; selectionRevision++ }
  }
  function bulkChanged(record: BulkRecord, value: BulkValue) {
    if (!consolidationAlive) return
    const old = bulkBatches.find((b) => b.key === record.key)?.value
    const next = { ...record, value }
    bulkBatches = bulkBatches.some((b) => b.key === record.key) ? bulkBatches.map((b) => b.key === record.key ? next : b) : [next, ...bulkBatches]
    if (record.roomId === roomId && record.roomRequest === roomRequest && record.selectionRevision === selectionRevision && value.started) {
      const byId = new Map(value.results.map((r) => [r.session, r]))
      setPicked(record.sessions.map((m) => m.id).filter((id) => { const result = byId.get(id); return !result || bulkRetryable(result, record.action) }))
      record.selectionRevision = selectionRevision
    }
    if (old?.busy && !value.busy && value.started && record.roomId === roomId && record.roomRequest === roomRequest) { loadMembers(); loadChanges() }
  }
  function openBulkAction(action: BulkAction, context: SelectionContext) {
    if (!currentSelection(context)) return
    const sessions = context.ids.map((id) => hierarchy.find((m) => m.id === id)).filter((m): m is Member & { id: string } => !!m?.id && selectableSession(m)).map((m) => ({ ...m }))
    if (!sessions.length) return
    detailRequest++
    const record = { key: ++bulkSerial, roomId, roomRequest, selectionRevision, sessions, action }
    const controller = createBulkSessionAction(api, record, (value) => bulkChanged(record, value))
    bulkControllers.set(record.key, controller)
    activeBulk = record.key
    controller.load()
  }
  function runBulk(key: number, force = false) { bulkControllers.get(key)?.run(force) }
  function selectBulkFailures(batch: BulkRecord) {
    if (batch.roomId !== roomId) return
    const failed = (batch.value?.results ?? []).filter((r) => bulkRetryable(r, batch.action)).map((r) => r.session)
    setPicked(pruneSelection(failed, hierarchy)); selectingSessions = true
    activeBulk = 0
  }
  function selectionActions(x: number, y: number, anchor?: HTMLElement) {
    selectionMenu = { x, y, anchor, roomId, ids: [...pickedSessions], key: selectionKey(roomId, pickedSessions) }
  }
  function closeSelectionMenu() { const anchor = selectionMenu?.anchor; selectionMenu = null; anchor?.focus?.() }
  function currentSelection(context: SelectionContext | null | undefined) { return context?.roomId === roomId && context.key === selectionKey(roomId, pickedSessions) }
  function selectionItems(context: SelectionContext) {
    return [
      { label: 'Select all sessions', run: () => { if (currentSelection(context)) setPicked(people.filter(selectableSession).flatMap((m) => m.id ? [m.id] : [])) } },
      { label: 'Message', run: () => messageSelection(context) },
      { label: 'Stop', run: () => openBulkAction('stop', context) },
      { label: 'Transfer model…', run: () => openBulkAction('transfer', context) },
      { label: 'Promote to lead', run: () => openBulkAction('promote', context) },
      { label: 'Move under…', run: () => openBulkAction('adopt', context) },
      { label: 'Close sessions…', danger: true, run: () => openBulkAction('close', context) },
      { label: 'Consolidate…', hint: '2–16 sessions', disabled: context.ids.length < 2 || context.ids.length > 16, run: () => { if (currentSelection(context)) openConsolidation() } },
    ]
  }
  // moveUnder opens the bulk "Move under…" panel for one session.
  function moveUnder(m: Member) {
    if (!m.id) return
    setPicked(m.id ? [m.id] : [])
    openBulkAction('adopt', { roomId, ids: [m.id], key: selectionKey(roomId, [m.id]) })
  }
  function messageSelection(context: SelectionContext) {
    if (!currentSelection(context)) return
    const selected = context.ids.map((id) => hierarchy.find((m) => m.id === id)).filter((m): m is Member => !!m && selectableSession(m))
    clearItemLink()
    const focusRequest = ++detailRequest
    drill = null; card = null; reply = null; view = GENERAL
    const key = roomId + ':' + GENERAL
    const text = (drafts[key] ?? '').replace(/^\s*(?:@\S+(?:\s+|$))*/, '')
    drafts = { ...drafts, [key]: selected.map((m) => '@' + m.address).join(' ') + ' ' + text }
    selectingSessions = false; setPicked([])
    if (narrow) showMembers = false
    tick().then(() => { if (consolidationAlive && context.roomId === roomId && focusRequest === detailRequest) composer?.focus() })
  }
  function toggleSessionSelection() {
    selectingSessions = !selectingSessions
    if (!selectingSessions) { setPicked([]); selectionRevision++ }
  }
  function pickSession(m: Member, checked: boolean, begin = false) { if (!m.id) return; if (begin) selectingSessions = true; setPicked(toggleSelection(pickedSessions, { ...m, id: m.id }, checked)) }
  function openConsolidation() {
    const ids = pruneSelection(pickedSessions, hierarchy)
    if (ids.length < 2 || ids.length > 16) return
    detailRequest++
    const sessions = ids.flatMap((id) => { const member = hierarchy.find((m) => m.id === id); return member ? [member] : [] })
    consolidating = { roomId, sessions, key: ++consolidationView }
    menu = null
  }
  function consolidationChanged(context: { key: number; roomId: string }, operation: { roomId: string }) {
    if (!consolidationAlive || (!consolidating || context.key !== consolidating.key) || context.roomId !== roomId || operation.roomId !== roomId) return
    selectingSessions = false
    setPicked([])
  }
  function closeConsolidation() {
    consolidating = null
    consolidationView++
    loadMembers()
    if (drill) loadSession()
  }

  // Profiles change from the CLI or the Settings view; the composer asks
  // again each time "/" opens its list.
  function loadProfiles() {
    api.profiles().then((p: ProfileChoice[] | null) => (profiles = p ?? [])).catch(() => {})
  }

  const workflowData = createWorkflowData(api, {
    current: () => ({ room: roomId, visit: roomRequest, id: openRun, running: view === 'runs' && !!openRun && runDetail?.run?.status === 'running', visible: document.visibilityState === 'visible' }),
    list: (value) => { runs = value; runsError = '' },
    jobs: (page, append) => { jobs = applyListPage('jobs', page, append); jobsError = '' },
    patchJobs: (change) => { jobs = applyListItem('jobs', change, jobs) },
    jobsError: (error) => (jobsError = error),
    detail: (value) => { runDetail = value; runError = '' },
    listError: (error) => (runsError = error),
    detailError: (error) => (runError = error),
  })
  function loadRuns() { return workflowData.loadList() }
  function loadRun() { return workflowData.loadDetail() }

  async function pauseRun(run: RunSummary) {
    const wasPaused = run.paused
    await api.pauseRun(run.id, !wasPaused)
    say(wasPaused
      ? { tone: 'success', subject: `${run.name} resumed`, text: 'Its agents continue where they stopped.', action: { label: 'Open', run: () => showRun({ id: run.id }) } }
      : { tone: 'success', subject: `${run.name} is paused`, text: 'Running agents finish; no new ones start.', action: { label: 'Open', run: () => showRun({ id: run.id }) } })
    loadRuns()
    await loadRun()
  }
  async function stopJob(job: ShellJob) {
    await api.stopShell(roomId, job.id)
    say({ tone: 'success', subject: 'Stopped the shell job', text: clipText(job.name || job.command.join(' ')) })
    workflowData.loadJobs()
  }

  function showRun(r: { id: string }) {
    clearItemLink()
    detailRequest++
    drill = null
    view = 'runs'
    if (openRun !== r.id) runDetail = null
    openRun = r.id
    loadRun()
  }

  function loadQuestions() { return reload('questions', async (current: () => boolean) => {
    const request = ++questionsRequest
    const id = roomId
    if (!id) return
    try {
      const q = await api.questions(id)
      if (current() && id === roomId && request === questionsRequest) { questions = reconcileRows(questions, q?.[id] ?? []) }
    } catch (e) { if (current() && id === roomId && request === questionsRequest) loadError = String(e) }
  }) }
  function loadVisuals() { return reload('visuals', async (current: () => boolean) => {
    try {
      const list = await api.visuals(roomId)
      if (current()) {
        visuals = reconcileRows(visuals, list ?? [], visualKey)
        setVisuals(roomId, visuals)
      }
    } catch (e) { if (current()) loadError = String(e) }
  }) }

  async function answerQuestion(id: string, choice: string, text: string) {
    await api.answerQuestion(roomId, id, choice, text)
    await loadQuestions()
  }
  function openQuestion(post: Post) {
    openView('questions')
    questionFocus = questionID(post)
    questionFocusRequest++
  }

  function loadGoals() { return reload('goals', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    try { const list = await api.goals(id); if (current() && roomId === id) { goals = applyListPage('goals', list); await refreshFetchedGoal(current) } }
    catch (e) { if (current() && roomId === id) loadError = String(e) }
  }) }

  function loadRules() { return reload('rules', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    try {
      const [room, global] = await Promise.all([api.rules(id, false), api.rules(id, true)])
      if (!current() || roomId !== id) return
      const v = { room, global, hash: `${global.hash}:${room.hash}` }
      const next = nextRules(rules, v, rulesEditing)
      rules = next.shown
      rulesPending = next.pending
    } catch (e) { if (current() && roomId === id) loadError = String(e) }
  }) }

  // A goal link may name a goal that no loaded page holds, such as an old
  // done goal: load it, then select it.
  // It resolves to the goal, or null when it cannot load.
  async function openGoal(id: string) {
    openView('goals'); goalId = id; showPins = false
    const have = goalById(id)
    if (!id || have) return have ?? null
    const rid = roomId
    try {
      const g = await api.goal(rid, id)
      if (rid !== roomId || !g) return null
      fetchedGoal = g
      return g
    } catch (e) {
      if (rid === roomId) say({ tone: 'error', subject: 'Could not open the goal', text: errorText(e) })
      return null
    }
  }
  function goalById(id: string) {
    return goals.find((g) => g.id === id) ?? (fetchedGoal?.id === id ? fetchedGoal : undefined)
  }
  // The fetched goal follows its change events, as listed goals do.
  function trackFetchedGoal(change: ListChange | undefined) {
    if (!fetchedGoal || change?.id !== fetchedGoal.id) return
    fetchedGoalVersion++
    fetchedGoal = change.op === 'delete' ? null : (change.item as Goal | undefined) ?? fetchedGoal
  }
  // After a goals reload (also after a detail action), read it again.
  async function refreshFetchedGoal(current = () => true) {
    const g = fetchedGoal, rid = roomId, version = fetchedGoalVersion
    if (!g || goals.some((x) => x.id === g.id)) return
    try {
      const fresh = await api.goal(rid, g.id)
      if (current() && rid === roomId && fetchedGoal?.id === g.id && version === fetchedGoalVersion) fetchedGoal = fresh
    } catch { /* Keep the last copy; the next reload tries again. */ }
  }

  // Quiet: the server fetches on its own; this only reads the saved counts.
  // A failure of this background read shows no banner: a device without
  // the scope, or a busy engine, must not look like a broken room. The
  // sync popover reports errors when the owner acts.
  function loadSync() { return reload('sync', async (current: () => boolean) => {
    try {
      const s = await api.syncStatus()
      if (current()) syncStatus = s ?? {}
    } catch { /* quiet */ }
  }) }

  // Refresh and Export start in the background; the saved operation shows
  // the progress. An error, such as one already running, shows in the popover.
  async function syncAction(call: (roomId: string, repo: string) => Promise<unknown>) {
    syncNote = ''
    try {
      if (!room?.repo) return
      await call(roomId, room.repo)
    } catch (e) {
      syncNote = errorText(e)
    }
    loadSync()
  }

  function loadPins() { return reload('pins', async (current: () => boolean) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.pins(id)) ?? []
      if (current() && id === roomId) pins = reconcileRows(pins, list)
    } catch (e) {
      if (current()) loadError = String(e)
    }
  }) }

  function loadIssues(history = false) { return reload(history ? 'issueHistory' : 'issues', async (current: () => boolean) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.issues(id, history)) ?? []
      if (!current() || id !== roomId) return
      if (history) issueHistory = reconcileRows(issueHistory, list.filter((i: RoomIssue) => i.state !== 'open'))
      else issues = reconcileRows(issues, list)
    } catch (e) {
      if (current()) loadError = String(e)
    }
  }) }

  // Notices open the same Issues tab as the room navigation.
  function openIssues() { showPins = false; showMembers = false; openView('issues') }

  // One issue action: run the call, then refresh both lists.
  async function triageIssue(call: () => Promise<unknown>) {
    try {
      await call()
      loadError = ''
      loadIssues()
      loadIssues(true)
    } catch (e) {
      say({ tone: 'error', subject: 'Could not update the issue', text: errorText(e) })
    }
  }

  // Close needs a reason: the backend rejects an empty one.
  function askCloseIssue(i: RoomIssue) {
    ask = {
      title: 'Close issue',
      label: 'Reason',
      lines: [i.title],
      action: 'Close',
      submit: async (reason) => {
        ask = null
        await triageIssue(() => api.closeIssue(roomId, i.id, reason))
      },
    }
  }

  async function togglePin(p: Post) {
    try {
      if (pinnedIds.has(p.id)) await api.unpin(roomId, p.id)
      else await api.pin(roomId, p.id)
      loadError = ''
      loadPins()
    } catch (e) {
      loadError = String(e)
    }
  }

  // Pins and links open the channel that contains the requested post.
  async function jumpTo(id: string) {
    const request = ++detailRequest
    const rid = roomId
    drill = null
    card = null
    viewing = null
    // Leave the bottom first, so a live post does not trim what this loads.
    feedAtBottom = false
    if (!(feed?.posts ?? []).some(post => post.id === id)) await loadEarlier('around', id)
    if (request !== detailRequest || rid !== roomId) return
    clearItemLink()
    const post = (feed?.posts ?? []).find((p) => p.id === id)
    if (post && questionID(post)) { openQuestion(post); return }
    if (post && isShellResult(post)) { openJob = post.from.slice('shell:'.length); view = 'jobs'; return }
    // A voice call line has no channel view; Voice's call widget shows it.
    if (post && inChannel(post, VOICE)) return
    view = post ? homeChannel(post) : GENERAL
    await tick()
    if (request === detailRequest && rid === roomId) feedView?.jump(id)
  }

  const sessionData = createSessionData(api, {
    current: () => ({ room: roomId, visit: roomRequest, id: drill?.id ?? '', root: !!drill?.rootSeat, selection: String(sessionVisit), turn: sessionTurn, visible: sessionVisible(), live: session?.member?.state === 'live' }),
    transcript: showSession,
    queue: (value) => (queue = value),
    error: (error) => (sessionError = error),
  })
  function showSession(value: SessionData, older: boolean | string = false) {
    session = mergeSessionPage(session, value, older, sessionReadingHistory)
    sessionError = ''
    // The root seat opened before root had a session of its own. Once RootSession returns the real
    // session (root started, or the seat was released), continue as that member: its queue, paging and events.
    if (drill?.rootSeat && value.member?.id) {
      drill = { ...value.member, id: value.member.id }
      loadSession()
    }
  }
  function sessionVisible(tab = sessionTab) { return pageVisible && !(narrow && reviewOpen) && ['chat', 'computer', 'media'].includes(tab) }
  function loadSession() { return sessionData.load() }
  $effect(() => {
    const tab = sessionTab
    const visible = sessionVisible(tab)
    untrack(() => { if (drill && visible) loadSession() })
  })

  function loadEarlierSession(near = () => true) {
    if (!session?.earlier || sessionTurn) return Promise.resolve(false)
    return sessionData.earlier(session.cursor ?? '', near)
  }

  function loadNewerSession(near = () => true) {
    if (!session?.newer || sessionTurn) return Promise.resolve(false)
    return sessionData.newer(String(session.turns?.at(-1)?.n ?? ''), near)
  }
  function presentSession() { return sessionData.present() }

  async function unqueue(q: Receipt) {
    if (!drill) return
    await api.deletePendingSteer(drill.id, q.id)
    queue = queue.filter((x) => x.id !== q.id)
    say({ tone: 'success', subject: 'Removed the queued message', text: 'The turn will not see it.' })
  }

  async function dismissPendingPost(id: string, messageId: string) {
    await api.dismissPendingPost(id, messageId, true)
    if (drill?.id === id) queue = queue.filter((post) => post.id !== messageId)
    say({ tone: 'success', subject: 'Message dismissed', text: 'Its history stays available.' })
  }

  function markShown(list: Post[]) { return readMarker.mark(list) }

  // Only messages visible in the selected channel become read.
  $effect(() => {
    if (!drill && isChannel(view)) markShown(posts)
  })

  function loadReviewer() { return reload('reviewer', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    const status = await api.reviewerStatus(id).catch(() => null)
    if (current() && consolidationAlive && id === roomId) reviewer = status
  }) }

  // A burst of PR events refreshes the reviewer once.
  let reviewerTimer: ReturnType<typeof setTimeout> | undefined
  function reviewerSoon() {
    clearTimeout(reviewerTimer)
    reviewerTimer = setTimeout(loadReviewer, 250)
  }

  function loadChanges() { return reload('changes', async (current: () => boolean) => {
    const id = roomId
    try {
      const all = await api.changes(id)
      if (!current()) return
      changes = applyListPage('changes', all)
      prError = ''
    } catch (e) { if (current()) prError = errorText(e) }
  }) }

  // Read navigation here; list state reads inside loaders must not become
  // dependencies that schedule another refresh after every response.
  $effect(() => {
    roomId; view; drill; showRoomSettings; memberChips; sidebarChips; pageVisible
    untrack(() => {
      loadChanges()
      loadReviewer()
      loadGoals()
      loadRules()
      loadVisuals()
    })
  })

  function applyListPage<K extends ListKind>(kind: K, page: ListPage<ListKinds[K]>, append = false): ListKinds[K][] {
    const previous = listPages[kind]
    const merged = mergeListPage(previous as import('./lib/ui/listPages.ts').ListPageView<ListKinds[K]> | undefined, { ...page, items: page.items ?? [], older: page.older ?? false, cursor: page.cursor ?? '' }, append)
    if (kind === 'jobs') merged.items.sort((a, b) => (String('started' in b ? b.started : '')).localeCompare(String('started' in a ? a.started : '')))
    merged.items = reconcileRows(previous?.items ?? [], merged.items, kind === 'changes' ? 'key' : 'id')
    listPages = { ...listPages, [kind]: merged }
    return merged.items
  }

  function applyListItem<K extends ListKind>(kind: K, change: ListChange, rows: ListKinds[K][] | null): ListKinds[K][] {
    const previous = listPages[kind] ?? { items: rows ?? [] }
    const items = patchListRows(previous.items ?? [], change as RowChange<ListKinds[K]>, kind === 'changes' ? 'key' : 'id')
    if (kind === 'jobs') items.sort((a, b) => (String('started' in b ? b.started : '')).localeCompare(String('started' in a ? a.started : '')))
    listPages = { ...listPages, [kind]: { ...previous, items } }
    return items
  }

  async function loadOlderLists(kinds: ListKind[]) {
    if (olderLoading) return
    const id = roomId, visit = roomRequest
    olderLoading = true
    olderFailed = false
    const methods = { goals: api.goals, changes: api.changes }
    try {
      for (const kind of kinds) {
        const page = listPages[kind]
        if (!page?.older) continue
        if (kind === 'jobs') {
          await workflowData.loadJobs(page.cursor)
          continue
        }
        await reload.patch(kind, async (current: () => boolean) => {
          const selected = listPages[kind]
          if (!current() || id !== roomId || visit !== roomRequest || !selected?.older) return
          if (kind === 'goals') {
            const next = await api.goals(id, selected.cursor)
            if (!current() || id !== roomId || visit !== roomRequest) return
            goals = applyListPage('goals', next, true)
          } else if (kind === 'changes') {
            const next = await api.changes(id, selected.cursor)
            if (!current() || id !== roomId || visit !== roomRequest) return
            changes = applyListPage('changes', next, true)
          }
        })
      }
    } catch (e) { if (id === roomId && visit === roomRequest) { loadError = String(e); olderFailed = true } }
    finally { olderLoading = false }
  }

  let canLoadControls = $derived(visibleList('roomControls'))
  let canLoadComputers = $derived(visibleList('computers'))
  let canLoadMentions = $derived(visibleList('mentions'))
  $effect(() => {
    roomId; canLoadControls
    untrack(() => { if (canLoadControls) roomSessionController?.load() })
  })
  $effect(() => {
    roomId; canLoadComputers
    untrack(() => { if (canLoadComputers) loadComputers() })
  })
  $effect(() => {
    roomId; canLoadMentions
    untrack(() => { if (canLoadMentions) roomData.loadMentions() })
  })

  async function untrackPR(pr: ChangeRow) {
    if (!pr.key) return
    try {
      await api.untrackPR(roomId, pr.key)
      say({ tone: 'success', subject: `Dyna stopped watching #${pr.number}`, text: 'Its updates no longer arrive here.' })
    } catch (e) {
      say({ tone: 'error', subject: 'Could not stop watching that pull request', text: errorText(e) })
    }
    loadChanges()
  }

  // trackPR links a PR to a session from the pull-requests view.
  async function trackPR(number: number | string | undefined, m: Member) {
    if (number === undefined || !m.id) return
    try {
      await api.trackPR(roomId, m.id, Number(number))
      say({ tone: 'success', subject: `Dyna watches #${number}`, text: `Review and CI updates go to ${m.name}.` })
    } catch (e) {
      say({ tone: 'error', subject: 'Could not watch that pull request', text: errorText(e) })
    }
    loadChanges()
  }

  async function mentionReference(reference: Reference) {
    if (reference.kind === 'room_issue' && !reference.url) reference = { ...reference, url: `#room=${encodeURIComponent(roomId)}&view=issues` }
    openView('all')
    await tick()
    composer?.addReference(reference)
    composer?.focus()
  }

  async function resumePR(pr: ChangeRow) {
    if (!pr.key) return
    try {
      await api.resumePR(roomId, pr.key)
      say({ tone: 'success', subject: `Dyna watches #${pr.number} again`, text: 'Review and CI updates resume.' })
    } catch (e) {
      say({ tone: 'error', subject: 'Could not resume watching that pull request', text: errorText(e) })
    }
    loadChanges()
  }

  // applyOpened shows what api.openRoom returned for the open room.
  function applyOpened(o: Opened) {
    const f = mergeFeedPage(feed && !feedAtBottom ? { ...feed, historyLoaded: true } : feed, o.feed)
    f.posts = reconcileRows(feed && feed.room.id === f.room.id ? feed.posts ?? [] : [], f.posts ?? [])
    feed = f
    hierarchy = o.members ?? []
    noteParents(hierarchy)
    setPicked(pruneSelection(pickedSessions, hierarchy))
    view = channels.resolveView(view)
    members = hierarchy.filter((m) => showClosed || !m.closed)
    choices = o.choices ?? []
    pins = o.pins ?? []
    loadSync()
    loadIssues()
    loadIssues(true)
    runs = o.runs ?? null
    runsError = ''
    workflowData.loadJobs()
    loadQuestions()
  }

  // Each request adds one bounded older page. Existing posts keep their
  // objects and unread marks until the room is left.
  let earlierLoad: EarlierLoad | null = null
  const STEP = 50
  async function loadEarlier(direction = 'older', target = '', near = () => true): Promise<boolean> {
    const id = roomId
    const roomLoad = roomRequest
    if (earlierLoad?.id === id && earlierLoad.roomLoad === roomLoad) {
      if (earlierLoad.direction === direction) return earlierLoad.promise ?? false
      return earlierLoad.promise?.then(() => id === roomId && roomLoad === roomRequest && loadEarlier(direction, target, near)) ?? false
    }
    if (!feed || (direction === 'older' && !feed.earlier) || (direction === 'newer' && !feed.newer)) return false
    feedAtBottom = false
    const cursor = direction === 'older' ? feed.cursor ?? '' : direction === 'newer' ? feed.posts?.at(-1)?.id ?? '' : target ?? ''
    const request: EarlierLoad = { id, roomLoad, direction, promise: null }
    earlierLoad = request
    request.promise = (async () => {
      try {
        const page = direction === 'newer' ? await api.feedAfter(id, cursor, STEP)
          : direction === 'around' ? await api.feedAround(id, cursor, STEP)
          : await api.feedTail(id, direction === 'present' ? '' : cursor, STEP)
        if (id !== roomId || roomLoad !== roomRequest || !feed) return false
        if (!near()) return true
        if (direction === 'older' && feed.cursor !== cursor) return false
        feed = direction === 'around' ? { ...page, historyLoaded: true } : Reflect.apply(mergeFeedPage, undefined, [feed, page, direction]) as FeedPage
        return true
      } catch (e) {
        if (id === roomId && roomLoad === roomRequest) loadError = String(e)
        return false
      } finally {
        if (earlierLoad === request) earlierLoad = null
      }
    })()
    return request.promise
  }

  // selectRoom loads a room and returns whether its navigation is still current.
  // One call brings the first screen; the room list refreshes beside it.
  async function selectRoom(id: string, startView = GENERAL) {
    clearItemLink(id, startView)
    const request = ++detailRequest
    roomRequest++
    navOpen = false
    machineId = ''
    machineDetail = null
    machineError = ''
    machinesNav = !id
    if (id !== roomId) {
      showRoomSettings = false
      selectionMenu = null
      selectingSessions = false
      setPicked([])
      selectionRevision++
      activeBulk = 0
      consolidating = null
      consolidationView++
    }
    if (id !== roomId) { computers = null; computerFeed = {} }
    roomId = id
    // Goal IDs are per room; a goal fetched for a link belongs to the old one.
    fetchedGoal = null
    visitRoomControls(id)
    drill = null
    card = null
    session = null
    sessionTurn = 0
    openJob = ''
    reply = null
    reviewOpen = startView === 'files' || startView === 'github' || startView === 'issues'
    reviewTab = startView === 'github' ? 'prs' : startView === 'issues' ? 'issues' : 'files'
    view = ['all', 'owner', 'turns', 'files', 'github'].includes(startView) || (startView === 'terminal' && !hasTerminal) ? GENERAL : startView
    members = []
    hierarchy = []
    runs = null
    listPages = {}
    jobs = null
    jobsError = ''
    openRun = ''
    runDetail = null
    pins = []
    issues = []
    issueHistory = []
    questions = []
    visuals = []
    questionFocus = ''
    questionsRequest++
    prs = []
    reviewer = null
    changes = null
    goals = []
    goalId = ''
    rules = null
    rulesPending = null
    if (!id) {
      feed = null
      return true
    }
    if (!await roomData.open(0, true)) return false
    return request === detailRequest
  }

  // openMember shows a member's profile card. The owner has no card.
  async function openMember(m: Member | { rootSeat: true } | null | undefined) {
    if (m && 'rootSeat' in m && m.rootSeat) { openSession(m); return }
    detailRequest++
    if (!m || !('name' in m) || m.owner || !m.id) return
    card = m as Drill
    profile = null
    profileError = ''
    try {
      const p = await api.profile(m.id)
      if (card?.id === m.id) profile = p
    } catch (e) {
      profileError = String(e)
    }
  }

  // openAgent opens a session from a machine's usage rows and from the
  // sidebar. A session in another room opens that room first.
  async function openAgent(m: AgentRef) {
    navOpen = false
    if (m.roomId && m.roomId !== roomId) {
      const selecting = selectRoom(m.roomId)
      const request = detailRequest
      if (!await selecting || request !== detailRequest) return
    }
    openSession(m)
  }

  // openSession drills into a member's full session view.
  function openSession(source: Member | ChannelMember | AgentRef | { rootSeat: true } | null, turn = 0, initial: SessionData | null = null, readOnly = false) {
    if (!source) return
    const m = ('rootSeat' in source && source.rootSeat ? rootMember() : source) as Drill
    clearItemLink()
    detailRequest++
    sessionVisit++
    card = null
    showPins = false
    if (narrow) showMembers = false
    if (drill?.id !== m.id || sessionTurn !== turn) {
      session = null
      sessionError = ''
      sessionTab = 'chat'
      selectedThread = ''
      queue = []
    }
    drill = m
    readOnlyDm = readOnly
    sessionTurn = turn
    if (initial) {
      session = initial
      sessionError = ''
    } else loadSession()
    setTimeout(() => sessionView?.focus(), 0)
  }

  // The root seat has no session of its own to send to; @root reaches whoever holds it.
  async function sendToSession(text: string, atts: AttachmentRef[] = [], forwardOf = '') {
    if (!drill) return
    const res = drill.rootSeat ? await api.send(roomId, '@root ' + text, '', atts, forwardOf) : await api.sendToSession(drill.id, text, atts, forwardOf)
    openSession(drill)
    return res
  }

  function openAddress(addr: string) {
    if (!room || addr === room.name) {
      drill = null
      return
    }
    const m = members.find((x) => x.address === addr)
    if (m) openMember(m)
  }

  function mentionMember(m: Member) {
    card = null
    drill = null
    const cur = draft.value.trim()
    draft.value = `@${m.name} ${cur.startsWith('@') ? cur.replace(/^(@\S+\s*)+/, '') : cur}`.trimEnd() + ' '
    setTimeout(() => composer?.focus(), 0)
  }

  // startReply targets the post's sender. It replaces the draft's leading
  // recipients (a kept '@viewer ' included) and keeps any text already
  // written after them. A reply to your own post leaves the draft alone.
  // A reply to a root post targets @root: root is an external orchestrator
  // that watches the room, not you.
  function startReply(p: Post) {
    reply = p
    if (!room) return
    if (!p.owner) {
      const body = draft.value.replace(/^\s*(?:@\S+(?:\s+|$))*/, '')
      const target = p.from.startsWith(room.name + ':') ? p.from.slice(room.name.length + 1) : 'root'
      draft.value = `@${target} ${body}`
    } else if (!p.human) {
      const body = draft.value.replace(/^\s*(?:@\S+(?:\s+|$))*/, '')
      draft.value = `@root ${body}`
    }
    composer?.focus()
  }

  // send goes through Go's one router: a "/<profile> <task>" draft starts a
  // session, anything else posts.
  async function send(text: string, atts: AttachmentRef[] = [], forwardOf = '', references: Reference[] = []) {
    const sent = roomId
    const draftKey = roomId + ':' + view
    const res = await api.send(sent, text, reply?.id ?? '', atts, forwardOf, references)
    if (res?.kind === 'spawn') {
      drafts = { ...drafts, [draftKey]: '' }
      fresh = res.spawn.member.id ?? ''
      setTimeout(() => { if (fresh === (res.spawn.member.id ?? '')) fresh = '' }, 4000)
      loadMembers()
    } else if (res?.post?.message?.id) {
      reply = null
      // Keep the recipients so the next post goes to the same members.
      drafts = { ...drafts, [draftKey]: res.post.sticky || '' }
    }
    return res
  }

  let reopening = $state('')
  async function reopenSession(m: Member) {
    if (reopening || dismissing || !m.closed || !m.resumable) return
    const rid = roomId, visit = roomRequest, selection = sessionVisit
    reopening = m.id ?? ''
    try {
      if (!m.id) return
      const member = await api.reopenSession(rid, m.id)
      if (rid !== roomId || visit !== roomRequest) return
      if (drill?.id === m.id && selection === sessionVisit) {
        drill = member as Drill
        if (session && session.member?.id === m.id) session = { ...session, member }
        await loadSession()
      }
      await loadMembers()
      say({ tone: 'success', subject: `${m.name} is open`, text: 'Its earlier turns are in the transcript.', action: m.id ? { label: 'Open', run: () => openSession(member) } : undefined })
    } finally { reopening = '' }
  }

  let dismissing = $state('')
  // Take back seat: the owner gives the root seat back to the internal root
  // when an external orchestrator stopped without detaching.
  async function takeBackSeat() {
    const rid = roomId
    try {
      await api.returnSeat(rid)
    } catch (e) {
      say({ tone: 'error', subject: 'Could not take back the root seat', text: errorText(e) })
      return
    }
    if (rid === roomId) await loadMembers()
  }
  async function dismissSession(m: Member) {
    if (dismissing || reopening || !isFinished(m)) return
    const rid = roomId, visit = roomRequest, selection = sessionVisit
    dismissing = m.id ?? ''
    try {
      if (!m.id) return
      const member = await api.dismissSession(rid, m.id)
      if (rid !== roomId || visit !== roomRequest) return
      if (drill?.id === m.id && selection === sessionVisit) {
        drill = member as Drill
        if (session && session.member?.id === m.id) session = { ...session, member }
        await loadSession()
      }
      await loadMembers()
      say({ tone: 'success', subject: `${m.name} is closed`, text: 'Its transcript stays available under closed sessions.' })
    } finally { dismissing = '' }
  }

  // Expedite gives the goal's agents and PRs the first review and merge.
  // A goal that would replace another asks first.
  async function expediteGoal(g: Goal) {
    if (g.room && g.room !== roomId) return
    const action = expediteAction($expedite, g)
    if (action === 'replace') { confirm = { kind: 'expedite', goal: g, roomId }; return }
    await api.expediteGoal(roomId, action === 'stop' ? '' : g.id, false)
    say(action === 'stop'
      ? { tone: 'success', subject: 'Stopped expediting the goal', text: clipText(g.text) }
      : { tone: 'success', subject: 'Expedited the goal', text: clipText(g.text), action: { label: 'Open', run: () => openGoal(g.id) } })
    loadGoals()
  }
  async function linkPRGoal(change: ChangeRow, goalId: string) {
    const id = roomId
    // A GitHub-only row has no record to hold its goal yet. Track it for the owner first.
    if (change.number === undefined) return
    const row = change.key ? change : await api.trackPR(id, '', change.number)
    if (!row.key) return
    await api.setPRGoal(id, row.key, goalId)
    if (id === roomId) await loadChanges()
  }
  // One menu item for the goals an agent holds, or its nearest ancestor holds.
  // A session with one goal expedites it at once; several goals open a picker.
  function expediteItems(m: Member): MenuItem[] {
    if (m.closed) return []
    const held = heldGoals(m, hierarchy, knownGoals)
    const item = expediteMenuItem(held, $expedite.goalId)
    if (!item) return []
    if (item.mode === 'choose') return [{ label: item.label, hint: item.hint, run: () => (expediting = { member: m, goals: held }) }]
    const only = held[0]
    if (!only) return []
    return [{ label: item.label, run: () => expediteGoal(only).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) })) }]
  }
  // Stop expediting the room's goal, from the picker.
  function stopExpediting(goals: Goal[]) {
    const g = goals.find((x) => x.id === $expedite.goalId)
    if (g) expediteGoal(g).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) }))
  }

  // Session menu: safe actions first, then the two that stop work, set
  // apart in coral. Both of those ask first.
  function menuItems(m: Member) {
    const items: MenuItem[] = [
      { label: m.closed ? 'Open transcript' : 'Message', run: () => openSession(m) },
      { label: 'Open profile', run: () => openMember(m) },
    ]
    if (isFinished(m)) items.push({ label: dismissing === m.id ? 'Dismissing…' : 'Dismiss', run: () => dismissSession(m).catch((e) => say({ tone: 'error', subject: `Could not dismiss ${m.name}`, text: errorText(e) })) })
    if (m.closed && m.resumable) items.push({ label: reopening === m.id ? 'Reopening…' : 'Reopen', run: () => reopenSession(m).catch((e) => say({ tone: 'error', subject: `Could not reopen ${m.name}`, text: errorText(e) })) })
    // The internal root holds the root seat, so it is never consolidated.
    if (!m.closed && m.role !== 'root') items.push({ label: 'Consolidate sessions…', run: () => { selectingSessions = true; setPicked(m.id ? [m.id] : []); showMembers = true; showPins = false } })
    if (!m.closed) items.push({ label: 'Mention in composer', run: () => mentionMember(m) })
    items.push(...expediteItems(m))
    items.push({ label: 'Copy address', hint: m.address, run: () => copyText(m.address).then(
      () => say({ tone: 'success', subject: 'Copied the address', text: `Paste it to mention ${m.name}.` }),
      (e) => say({ tone: 'error', subject: 'Could not copy the address', text: errorText(e) }),
    ) })
    if (!m.closed) items.push({ label: m.transfer ? 'Model transfer…' : 'Transfer to new model…', run: () => { loadProfiles(); transferring = m } })
    if (!m.closed && m.parent) items.push({ label: 'Promote to lead', run: () => (confirm = { kind: 'promote', m }) })
    if (selectableSession(m)) items.push({ label: 'Move under…', run: () => moveUnder(m) })
    if (m.state === 'live') items.push({ label: 'Stop turn', danger: true, gap: true, run: () => (confirm = { kind: 'stop', m }) })
    if (!m.closed) items.push({ label: 'Close session', danger: true, gap: m.state !== 'live', run: () => (confirm = { kind: 'close', m }) })
    return items
  }

  // Room menu: everyday actions first, then Delete in coral, which asks for
  // the room's name.
  function roomMenuItems(r: Room) {
    return roomActionItems(actionRegistry(r))
  }

  async function roomAction(fn: () => Promise<unknown>, done: string) {
    try {
      await fn()
      say({ tone: 'success', subject: done })
      await loadRooms()
    } catch (e) {
      say({ tone: 'error', subject: 'Could not finish that room action', text: errorText(e) })
    }
  }

  function renameRoom(r: ActionRoom) {
    ask = {
      title: 'Rename room',
      label: 'Name',
      value: roomLabel(r),
      lines: [`The new name shows in the app. Agents still address the room as #${r.name}.`],
      action: 'Rename',
      submit: async (name) => {
        await api.renameRoom(r.id, name)
        ask = null
        say({ tone: 'success', subject: 'Room renamed', text: `It now shows as ${name}.` })
        loadRooms()
      },
    }
  }

  function deleteRoom(r: ActionRoom) {
    ask = {
      title: `Delete ${roomLabel(r)}?`,
      lines: ['This removes the room, its feed and its pins. Records of closed sessions stay.', 'A room with live work cannot be deleted. Stop its sessions and runs first.', `Type ${r.name} to confirm.`],
      label: 'Room name',
      placeholder: r.name,
      match: r.name,
      action: 'Delete room',
      danger: true,
      submit: async () => {
        await api.deleteRoom(r.id)
        ask = null
        say({ tone: 'success', subject: 'Room deleted', text: `${roomLabel(r)} and its feed are gone.` })
        if (roomId === r.id) selectRoom('')
        loadRooms()
      },
    }
  }

  function steerAgent(run: RunSummary, a: RunAgent) {
    ask = {
      title: `Steer ${a.label || 'agent ' + a.id}`,
      lines: [`This message goes to this agent of ${run.name} while it works.`],
      multiline: true,
      placeholder: 'What should it do differently?',
      action: 'Send',
      submit: async (text) => {
        await api.steerRun(run.id, a.id, text)
        ask = null
        say({ tone: 'success', subject: 'Steering sent', text: 'The agent sees it while it works.' })
      },
    }
  }

  async function runStarted(id?: string) {
    starting = false
    say({ tone: 'success', subject: 'Workflow started', text: 'Its agents run now. Open Runs to follow it.', action: id ? { label: 'Open', run: () => showRun({ id }) } : undefined })
    await loadRuns()
    if (id) showRun({ id })
  }

  function closeMenu() {
    const anchor = menu?.anchor
    menu = null
    anchor?.focus?.()
  }

  let toastTimer: ReturnType<typeof setTimeout> | undefined
  // A toast reports the result of one action the user just took: a subject, one
  // sentence, and at most one thing to do next. Call sites pass display names.
  function say(input: ToastInput) {
    const next = makeToast(input)
    hideToast()
    toast = next
    clearTimeout(toastTimer)
    toastTimer = setTimeout(hideToast, dismissDelay(next))
  }
  function hideToast() {
    toast = null
    toastDetail = null
    toastDetailOpen = false
    clearTimeout(toastTimer)
  }
  // runToastAction closes the toast, then follows the reader's choice.
  function runToastAction(run: () => void) {
    hideToast()
    run()
  }
  // sayLine lets the command registry report a finished command as one
  // success line, as it did before toasts had a tone.
  function sayLine(message: string) {
    say({ tone: 'success', subject: message })
  }
  // sayDetails reports a finished bulk action in one line. The per-session
  // list stays behind Details, so it never pushes the members out of view.
  function sayDetails(summary: RoomControlSummary) {
    say({ subject: summary.text })
    toastDetail = { title: summary.title, items: summary.details }
    clearTimeout(toastTimer)
    toastTimer = setTimeout(hideToast, TOAST_DETAIL_MS)
  }
  // toggleToastDetail keeps the one line on screen while its list is open.
  function toggleToastDetail() {
    if (toastDetailOpen) { closeToastDetail(); return }
    toastDetailOpen = true
    clearTimeout(toastTimer)
  }
  // closeToastDetail leaves the one line on screen for its normal time again.
  function closeToastDetail() {
    toastDetailOpen = false
    clearTimeout(toastTimer)
    toastTimer = setTimeout(hideToast, toast?.action ? dismissDelay(toast) : TOAST_MS)
  }

  // guardItems lists what a close-guard error names: "responsibilities: cannot close:\n- ...".
  function guardItems(msg: string) {
    const items = msg.split('\n').filter((l) => l.startsWith('- ') && !l.startsWith('- or close anyway')).map((l) => l.slice(2))
    return items.length ? items : [msg]
  }

  let confirmText = $derived.by(() => {
    if (!confirm) return null
    if (confirm.kind === 'promote') {
      return {
        title: `Promote ${confirm.m.name} to lead?`,
        lines: ['This session becomes a top-level lead. Its children stay attached, with updated depths.', 'Its active work and full conversation continue.'],
        action: 'Promote to lead', danger: false,
      }
    }
    if (confirm.kind === 'forceclose') {
      return {
        title: `Could not hand over all work from ${confirm.m.name}`,
        lines: [...confirm.items, 'Retry after fixing the error, or force close. Goals move to the owner; other work stays unresolved.'],
        action: 'Close anyway',
      }
    }
    if (confirm.kind === 'expedite') {
      return {
        title: `Expedite goal ${confirm.goal.id}?`,
        lines: [`Goal ${$expedite.goalId} is expedited now. A room has one expedited goal, so this replaces it.`],
        action: 'Replace and expedite', danger: false,
      }
    }
    if (confirm.kind === 'cancelrun') {
      return {
        title: `Cancel ${confirm.run.name}?`,
        lines: ['Its running agents stop now. Work they finished stays in the run record.'],
        action: 'Cancel run',
      }
    }
    const m = confirm.m
    if (confirm.kind === 'stop') {
      return {
        title: `Stop the turn of ${m.name}?`,
        lines: ['The running turn stops now, and its unfinished work is lost.', 'The session stays open. Your next post to it starts a new turn.'],
        action: 'Stop turn',
      }
    }
    const lines = ['The session closes, and its name becomes free.', 'PR authorship moves to its nearest open ancestor, or root. Reviewer roles return to the pool. Live workflows are canceled.']
    lines.push('A held goal goes up with the work, but root cannot hold a goal. A goal with only root or the owner above it stops the close.')
    lines.push('“Close and move goals” closes anyway and gives those goals to the owner. Other unfinished work then stays unresolved.')
    if (m.state === 'live') lines.push('Its running turn stops first.')
    const hasChildren = members.some((child) => !child.closed && child.parent === m.id)
    if (hasChildren) lines.push('Its open children that own no work close too. Children that still own work move to its nearest open ancestor and keep running.')
    return { title: `Close ${m.name}?`, lines, action: 'Close session', alternative: 'Close and move goals', onalternative: () => runConfirm(true) }
  })

  async function runConfirm(force = false) {
    const request = confirm
    if (!request) return
    const { kind } = request
    confirm = { ...request, busy: true, error: '' }
    if (kind === 'expedite') {
      try {
        await api.expediteGoal(request.roomId, request.goal.id, true)
        say({ tone: 'success', subject: 'Expedited the goal', text: clipText(request.goal.text) })
        confirm = null
        loadGoals()
      } catch (e) {
        confirm = { ...request, busy: false, error: errorText(e) }
      }
      return
    }
    if (kind === 'cancelrun') {
      try {
        await api.cancelRun(request.run.id)
        say({ tone: 'success', subject: `${request.run.name} is canceled`, text: 'Work it finished stays in the run record.' })
        confirm = null
        loadRuns()
        loadRun()
      } catch (e) {
        confirm = { ...request, busy: false, error: errorText(e) }
      }
      return
    }
    const m = request.m
    if (!m.id) return
    try {
      if (kind === 'stop') await api.stopSession(roomId, m.id)
      else if (kind === 'promote') await api.promoteSession(roomId, m.id)
      else {
        const result = await api.closeSession(roomId, m.id, force || kind === 'forceclose')
        closeResult = { name: m.name, items: result?.closeSummary ?? [] }
      }
      confirm = null
      if (kind === 'stop') say({ tone: 'success', subject: 'Turn stopped', text: `${m.name} stays open; your next post starts a new turn.` })
      else if (kind === 'promote') say({ tone: 'success', subject: `${m.name} is now a top-level lead`, text: 'Its children stay attached, with new depths.' })
      else say({ tone: 'success', subject: `${m.name} is closed`, text: 'Its PR authorship and reviewer roles have moved on.' })
      loadMembers()
      loadChanges()
    } catch (e) {
      const msg = errorText(e)
      // Force is offered only when the automatic handoff fails.
      if (kind === 'close' && msg.startsWith('responsibilities:')) confirm = { kind: 'forceclose', m, items: guardItems(msg) }
      else confirm = { ...request, busy: false, error: msg }
    }
  }

  let roomTimer: ReturnType<typeof setTimeout> | undefined
  const roomRefreshes = new Set<string>()
  function soonRooms(kind = 'rooms') {
    roomRefreshes.add(kind)
    clearTimeout(roomTimer)
    roomTimer = setTimeout(() => {
      const pending = new Set(roomRefreshes)
      roomRefreshes.clear()
      if (pending.has('rooms')) loadRooms()
      if (pending.has('feed') && roomId) roomData.loadUnread()
      if (pending.has('controls')) roomSessionController?.load()
      // A member row's responsibility count changes with goals, questions,
      // PRs, shells and runs, not with the member itself.
      if (pending.has('members') && roomId) loadMembers()
    }, 250)
  }

  function refreshVisibleRoom() {
    loadRooms()
    if (!roomId) return
    loadMembers()
    loadQuestions()
    loadPins()
    loadSync()
    loadIssues()
    loadIssues(true)
    if (drill && sessionVisible()) loadSession()
  }

  // resync reloads what the app shows after the web event stream was lost:
  // events may have been missed while it was down.
  async function resync() {
    mergeVersion += 1
    loadRooms()
    if (!roomId) return
    // Keep loaded history on screen when the event stream reconnects.
    if (!await roomData.open(Math.max(feed?.posts?.length ?? 0, 50) + 20)) return
    roomSessionController?.load()
    if (drill) { loadSession(); reviewVersion++ } // events may have been missed while the stream was down
    if (openRun) loadRun()
    loadChanges()
    loadReviewer()
    loadGoals()
    loadRules()
    loadVisuals()
  }

  // A push (or a link to #room=<id>&post=<id>) names a room to open. Only
  // ids in the expected shape get through.
  async function openTarget(id: string, post: string | null | undefined, startView = GENERAL) {
    if (!ROOM.test(id || '') || !rooms.some((r) => r.id === id)) return false
    const selecting = selectRoom(id, startView)
    const request = detailRequest
    if (!await selecting || request !== detailRequest || id !== roomId) return false
    if (post && POST.test(post)) jumpTo(post)
    return { roomId: id, request: detailRequest }
  }

  function hashTarget() {
    const target = parseDetailHref(location.hash)
    const h = new URLSearchParams(location.hash.slice(1))
    // A pairing link opened on a device that is already paired: drop the
    // code. An unpaired device's pairing screen reads it instead.
    if (h.has('pair') && !needPair) history.replaceState(null, '', location.pathname)
    const id = h.get('room')
    if (!id) return false
    callPush = callTarget(location.hash) ?? callPush
    const pr = parsePRHash(location.hash)
    const issue = parseIssueHash(location.hash)
    const file = parseFileHash(location.hash)
    const r = parseRoute(location.hash)
    // Item panes follow their own links in the same conversation.
    if (id === roomId && routeSession(drill) === r?.session && ((pr && reviewOpen && reviewTab === 'prs') || (issue && view === 'issues') || (file && reviewOpen && reviewTab === 'files'))) return true
    if (!r) return false
    const post = h.get('post')
    const item = pr || issue || file
    // An item link stays in the address bar, so a reload opens the same item.
    // Any other link becomes the address of the screen it opens.
    if (!item && location.hash !== routeHash(r)) history.replaceState(null, '', location.pathname + location.search + routeHash(r))
    routing++
    // A shell link names no screen of its own, so it always reaches openDetail.
    const opening = id === roomId && !post && !item && target?.kind !== 'shell' ? Promise.resolve(routeHere(r)) : openTarget(id, post, issue ? 'issues' : pr ? 'github' : file ? 'files' : r.view)
    opening.then(async (opened) => {
      if (!opened || opened.roomId !== roomId || opened.request !== detailRequest) return
      if (target?.kind === 'shell') await openDetail(target)
      else if (r.session === ROOT_SESSION) openSession({ rootSeat: true })
      else if (r.session || r.run) await openDetail({ kind: r.session ? 'session' : 'run', id: r.session || r.run, turn: r.turn })
      if (r.review) openReview(r.review)
      if (file) openReview('files')
      if (pr || issue) {
        openView(issue ? 'issues' : 'github')
        prLink = issue || pr
      }
    }).finally(() => {
      routing--
      routeFix = true
    })
    return true
  }

  async function signOut() {
    try {
      await logout()
    } finally {
      location.reload()
    }
  }

  function applyItemEvent(ev: StreamEvent) {
    if (!('change' in ev) || !(ev.change?.op === 'delete' || ev.change?.op === 'upsert' && ev.change.item) || !roomId || ev.roomId !== roomId) return false
    const change = ev.change
    if (!change) return false
    if (ev.kind === 'members') {
      roomData.patchMembers(ev.change)
      return true
    }
    // The PR rows come from Changes; a prs event repeats them.
    if (ev.kind === 'prs') return true
    if (ev.kind === 'changes') {
      reload.patch('changes', (current) => {
        if (current()) changes = applyListItem('changes', change, changes)
      })
      return true
    }
    if (ev.kind === 'jobs') {
      workflowData.patchJobs(ev.change)
      return true
    }
    if (ev.kind === 'questions') {
      reload.patch('questions', (current) => {
        if (current()) questions = patchListRows(questions, change as RowChange<Question>)
      })
      return true
    }
    if (ev.kind === 'goals') {
      reload.patch('goals', (current) => {
        if (!current()) return
        goals = applyListItem('goals', change, goals)
        trackFetchedGoal(change)
      })
      return true
    }
    return false
  }

  function handle(ev: StreamEvent) {
    const evRoom = 'roomId' in ev ? ev.roomId : undefined
    if (['prs', 'changes', 'resync'].includes(ev.kind)) refreshHeldComments()
    const item = applyItemEvent(ev)
    home?.event(ev)
    if (ev.kind === 'computer') return handleComputer(ev)
    if (ev.kind === 'resync') return resync()
    if (ev.kind === 'questions' && evRoom === roomId && !item) loadQuestions()
    if (reloadsUnread(ev, roomId, item)) soonRooms('feed')
    if (ev.kind === 'posts' && evRoom === roomId) {
      if (changesVisuals(ev)) loadVisuals()
      if (roomSessionController && (roomControls?.loading || roomControls?.state?.paused !== false)) roomSessionController.load()
    }
    if (ev.kind === 'posts' && feed && evRoom === feed.room.id) {
      const seen = new Set((feed.posts ?? []).map((p) => p.id))
      const add = (ev.posts ?? []).filter((p) => !seen.has(p.id))
      if (add.length) {
        if (feed.newer || !feedAtBottom) feed = { ...feed, newer: (feed.newer ?? 0) + add.length }
        else {
          feed.posts = [...(feed.posts ?? []), ...add]
          if (feedAtBottom) feed = trimFeed(feed)
        }
      }
    }
    if (ev.kind === 'goals' && evRoom === roomId) { if (!item) loadGoals(); loadRules() }
    if (ev.kind === 'machines') { if (machinesVisible()) loadMachines(); if (machineId) loadMachine(machineId) }
    if (ev.kind === 'pins' && evRoom === roomId) loadPins()
    if (ev.kind === 'issues' && evRoom === roomId) { loadIssues(); loadIssues(true) }
    if (['room.paused', 'room.resumed'].includes(ev.kind) && (!evRoom || evRoom === roomId)) roomSessionController?.load()
    // A prs event without a row says the room's PR settings changed.
    if ((ev.kind === 'prs' || ev.kind === 'changes') && (!evRoom || evRoom === roomId)) {
      mergeVersion += 1
      reviewerSoon()
      if (ev.kind === 'changes' && !item) loadChanges()
    }
    if (ev.kind === 'session' && evRoom === roomId && ev.change?.id === drill?.id) { loadSession(); reviewVersion++ }
    if (ev.kind === 'members' && evRoom === roomId) {
      if (!item) loadMembers()
      if (ev.change?.controls) soonRooms('controls')
    }
    if (ev.kind === 'posts' && drill && evRoom === roomId) {
      sessionData.schedule()
    }
    if (['goals', 'questions', 'prs', 'jobs', 'runs'].includes(ev.kind) && (!evRoom || evRoom === roomId)) soonRooms('members')
    if (ev.kind === 'jobs' && evRoom === roomId && !item) workflowData.loadJobs()
    if (ev.kind === 'runs' && evRoom === roomId) {
      loadRuns()
      if (openRun) loadRun()
    }
    // Only these events change room names, unread posts or session counts.
    if ((ev.kind === 'members' && !item) || ['rooms', 'posts', 'room.paused', 'room.resumed'].includes(ev.kind)) soonRooms()
  }

  // Ctrl+B toggles the room sidebar, as in most editors. It works from the
  // composer too; a textarea has no use for the key.
  function shortcut(e: KeyboardEvent) {
    const inside = (selector: string) => !!(e.target as Element | null)?.closest?.(selector)
    if (e.key === 'Escape' && (e.defaultPrevented || inside('[role="menu"], [role="dialog"], [role="alertdialog"]'))) return
    if (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      if (showPalette) closeSearch()
      else openSearch()
      return
    }
    if (e.key.toLowerCase() === 'b' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      showSide = !showSide
    }
    if (e.key === 'Escape' && selectingSessions) { e.preventDefault(); toggleSessionSelection(); return }
    if (e.key === 'Escape' && drill && threadsOpen && !viewing && !card && !showPalette) { threadsOpen = false; return }
    // Esc closes the panel unless a modal on top of it owns the key.
    if (e.key === 'Escape' && goalId && !viewing && !card && !showPalette) goalId = ''
    if (e.key === 'Escape' && showPins && !viewing && !card && !showPalette) showPins = false
    if (e.key === 'Escape' && narrow) {
      navOpen = false
      showMembers = false
    }
  }

  let searchText = $state('')
  function openSearch(query = '') {
    searchText = query
    showPalette = true
  }
  function closeSearch() {
    showPalette = false
    palettePath = null
    searchText = ''
  }

  // The palette's own commands. "New room" is built into the palette.
  // Icons for palette commands, as SVG paths on a 20 px grid.
  const ICON = {
    files: 'M3 5.5a1.5 1.5 0 0 1 1.5-1.5h3.4l1.6 1.8h6a1.5 1.5 0 0 1 1.5 1.5v7.2a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 3 14.5Z',
    terminal: 'M3.5 4h13a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1ZM6 8l2.5 2L6 12M10.5 12.5H14',
    jobs: 'M7.5 5.5h9M7.5 10h9M7.5 14.5h9M3.5 5.5h.01M3.5 10h.01M3.5 14.5h.01',
    github: 'M6 4.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM6 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM17 15.5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0ZM4.5 6v8M15.5 14V8.5a2.5 2.5 0 0 0-2.5-2.5h-3M11.5 4 9.5 6l2 2',
    run: 'M6.5 4.5v11l9-5.5Z',
    stop: 'M5.5 5.5h9v9h-9Z',
    flows: 'M3.5 4h5v4.5h-5ZM11.5 11.5h5V16h-5ZM6 8.5v2.5a1.5 1.5 0 0 0 1.5 1.5h4',
    pin: 'M8 3.5h4l-.6 4.2 2.6 2.3H6l2.6-2.3ZM10 10v6.5',
    rename: 'M4 16h3l8.5-8.5-3-3L4 13Z',
    archive: 'M3.5 4.5h13v3h-13ZM4.5 7.5v8h11v-8M8 10.5h4',
    trash: 'M4.5 6h11M8 6V4.5h4V6M6 6l.7 10h6.6L14 6',
    device: 'M7 3h6a1.5 1.5 0 0 1 1.5 1.5v11A1.5 1.5 0 0 1 13 17H7a1.5 1.5 0 0 1-1.5-1.5v-11A1.5 1.5 0 0 1 7 3ZM9 14.5h2',
    gear: 'M10 12.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5ZM10 2.5v2M10 15.5v2M2.5 10h2M15.5 10h2M4.7 4.7l1.4 1.4M13.9 13.9l1.4 1.4M4.7 15.3l1.4-1.4M13.9 6.1l1.4-1.4',
    side: 'M3.5 4.5h13v11h-13ZM8 4.5v11',
  }

  function openPR(n: number | string) {
    history.pushState({ pr: true }, '', prHash(roomId, n, 'conversation', routeSession(drill)))
    openView('github')
    prLink = { roomId, pr: String(n), tab: 'conversation' }
  }

  // Leaving an item's room or view drops its link. The item's history entry
  // stays, so Back reopens it; syncRoute gives the new screen its own entry.
  function clearItemLink(nextRoom = '', nextView = '') {
    const issue = parseIssueHash(location.hash)
    const link = issue || parsePRHash(location.hash)
    if (link && (link.roomId !== nextRoom || nextView !== (issue ? 'issues' : 'github'))) prLink = null
  }

  // An open PR, issue or file keeps its own, more exact address.
  function holdsItemLink() {
    if ((new URLSearchParams(location.hash.slice(1)).get('session') ?? '') !== routeSession(drill)) return false
    const issue = parseIssueHash(location.hash)
    const pr = parsePRHash(location.hash)
    const file = parseFileHash(location.hash)
    const link: [string, { roomId: string }] | null = issue ? ['issues', issue] : pr ? ['github', pr] : file ? ['files', file] : null
    return !!link && link[1].roomId === roomId && (link[0] === 'issues' ? view === 'issues' : reviewOpen && reviewTab === (link[0] === 'files' ? 'files' : 'prs'))
  }

  // syncRoute puts the open screen's address in the address bar
  // (lib/ui/route.ts). A followed link passes through other screens while it
  // loads, so routing holds the address until it lands. The first write, and
  // the one after a followed link, correct the current entry instead of adding one.
  function syncRoute() {
    // A service's read-only conversation keeps the Services address.
    const session = readOnlyDm ? '' : routeSession(drill)
    const hash = routeHash({ room: roomId, view, session, turn: session ? sessionTurn : 0, run: view === 'runs' ? openRun : '', review: reviewOpen ? reviewTab : undefined })
    if (!started || routing || pop) return
    // An item link is already the right entry; the next screen adds its own.
    if (!holdsItemLink()) writeRoute(hash, routeFix, location, history)
    routeFix = false
  }

  // Back or Forward to the all-rooms page leaves no address. The hash is read
  // first: hashTarget drops a pairing code from it.
  function hashChanged() {
    const empty = !location.hash
    if (!hashTarget() && empty && roomId) selectRoom('')
  }

  // routeHere follows an address in the open room without reloading the room.
  // It returns false when the screen already shows the address.
  function routeHere(r: Route): { roomId: string; request: number } | false {
    // A followed address always ends a detail navigation that is still loading:
    // its older response must not open over the screen the reader asked for.
    detailRequest++
    const sameChat = r.session ? routeSession(drill) === r.session && sessionTurn === r.turn : r.run ? !drill && view === 'runs' && openRun === r.run : !drill && !openRun && view === r.view
    if (sameChat && (r.review ?? '') === (reviewOpen && r.view !== 'issues' ? reviewTab : '')) return false
    reviewOpen = false
    if (!r.session && !r.run) openView(r.view)
    if (r.review) openReview(r.review)
    return { roomId, request: detailRequest }
  }

  function openReview(tab = reviewTab) {
    detailRequest++
    if (tab !== reviewTab) prLink = null
    reviewTab = tab
    reviewOpen = true
    threadsOpen = false
    reviewVersion++
    showMembers = false
    showPins = false
    navOpen = false
    if (!drill && tab === 'issues') view = 'issues'
    else if (!drill && !isChannel(view)) view = GENERAL
    loadChanges()
    loadReviewer()
  }

  function closeReview() {
    reviewOpen = false
    prLink = null
    if (view === 'issues') view = GENERAL
    writeRoute(routeHash({ room: roomId, view, session: routeSession(drill), turn: sessionTurn, run: '' }), true, location, history)
  }

  function openView(v: string) {
    if (v === 'files' || v === 'github') { openReview(v === 'files' ? 'files' : 'prs'); return }
    reviewOpen = false
    clearItemLink(roomId, v)
    detailRequest++
    prLink = null
    drill = null
    openRun = ''
    reveal = ''
    revealFolder = null
    openJob = ''
    if (view !== v) reply = null
    // A new view opens at the bottom, so older posts can go. Selecting the
    // open view again keeps the reader's place, so it trims only at the bottom.
    const next = channels.resolveView(v)
    if (next !== view || feedAtBottom) feed = trimFeed(feed)
    view = next
    if (next === 'issues') openReview('issues')
    if (v !== 'goals') goalId = ''
    navOpen = false
    if (v === 'runs') loadRuns()
  }

  // Completion links open stored UI details. They never invoke a command.
  async function openDetail(target: DetailTarget) {
    let request = ++detailRequest
    const rid = target.roomId || roomId
    card = null
    navOpen = false
    if (narrow) {
      showMembers = false
      showPins = false
    }
    if (rid !== roomId) {
      const selecting = selectRoom(rid)
      request = detailRequest
      if (!await selecting || request !== detailRequest || rid !== roomId) return
    }
    if (target.kind === 'shell') {
      openView('jobs')
      openJob = target.id
    } else if (target.kind === 'run') {
      showRun({ id: target.id })
    } else if (target.kind === 'session') {
      try {
        const data = target.turn ? await api.transcriptTurn(target.id, target.turn) : await api.transcript(target.id)
        if (rid === roomId && request === detailRequest) {
          sessionTab = 'chat'
          openSession(data.member, target.turn, data)
        }
      } catch (e) {
        if (request === detailRequest) say({ tone: 'error', subject: 'Could not open that session', text: errorText(e) })
      }
    }
  }

  function openSettings(start = 'profiles', pairNow = false, profile = '', accountId = '') {
    navOpen = false
    settingsAt = { start, pairNow, profile, accountId }
    settingsKey++
    showSettings = true
  }

  // A hook card asks for its run's log. Hooks belong to the repository, and
  // Room settings manages them for every room on it, so the modal opens on
  // Hooks with the repo and the run already in front, as `dyna git hooks log` reads.
  // The pane reports a run this room's repository does not hold.
  function openHookLog(target: HookLogTarget) {
    navOpen = false
    if (!room) { say({ tone: 'info', subject: 'Open a room first', text: 'Hook runs belong to a room repository.' }); return }
    showSettings = false
    roomSettingsAt = { start: 'hooks', hookRun: target }
    roomSettingsKey++
    showRoomSettings = true
  }

  function createProfile(selection: ModelSelection) {
    settingsAt = { start: 'profiles', pairNow: false, initialSelection: selection }
    settingsKey++
    showSettings = true
  }

  // Slash argument parsing stays here; menu and palette actions call the
  // same handlers with no argument.
  let templates = $state<{ name: string; description?: string }[] | null>(null)
  function loadTemplates() {
    if (!templates) api.listTemplates().then((t: { name: string; description?: string }[] | null) => (templates = t ?? []), () => (templates = []))
    return templates ?? []
  }
  function actionRegistry(r = current ?? room) {
    return actions({
      room: r, rooms, icons: ICON, hasTerminal, narrow, runs, session: drill, sessionPRs: prMap[drill?.id ?? ''],
      openView, openSettings, renameRoom, deleteRoom, roomAction, say: sayLine, loadRooms,
      openFiles: (path) => { openView('files'); if (path) reveal = path.replace(/^\.?\//, '') },
      openPins: () => { showPins = true; if (narrow) showMembers = false },
      openSearch: (arg) => openSearch(arg || ''),
      openRoomSettings: async (target) => { if (target.id === roomId || await selectRoom(target.id)) { roomSettingsAt = { start: 'general' }; roomSettingsKey++; showRoomSettings = true } },
      openSessionPRs: () => openReview('prs'),
      cancelRun: (run) => (confirm = { kind: 'cancelrun', run }),
      toggleSidebar: () => (narrow ? (navOpen = !navOpen) : (showSide = !showSide)),
      roomOptions: () => rooms.filter((x) => x.id !== r?.id).map((x) => ({ value: x.name, hint: x.displayName && x.displayName !== x.name ? x.displayName : x.repo || '' })),
      switchRoom: (arg) => {
        if (!arg) return void (showPalette = true)
        const a = arg.toLowerCase()
        const hit = rooms.find((x) => x.name.toLowerCase() === a || roomLabel(x).toLowerCase() === a) ?? rooms.find((x) => `${x.name} ${roomLabel(x)}`.toLowerCase().includes(a))
        if (!hit) return `No room matches "${arg}".`
        selectRoom(hit.id)
      },
      newRoom: (arg) => { palettePath = arg || ''; showPalette = true },
      templateOptions: () => loadTemplates().map((t) => ({ value: t.name, hint: t.description || '' })),
      startWorkflow: async (arg) => {
        if (!arg) return void (openView('runs'), (starting = true))
        const name = arg.split(/\s+/)[0] ?? ''
        const text = arg.slice(name.length).trim()
        let input = null
        if (text) {
          try { input = JSON.parse(text) } catch { input = text }
        }
        if (!r) return
        const res = await api.startRun(r.id, { template: name }, input)
        say({ tone: 'success', subject: `${name} started`, text: 'Its agents run now.', action: res?.id ? { label: 'Open', run: () => showRun({ id: res.id }) } : undefined })
        await loadRuns()
        if (res?.id) showRun({ id: res.id })
      },
    })
  }
  let registry = $derived(actionRegistry())
  let paletteCommands = $derived(actionItems(registry, 'palette'))
  let composerCommands = $derived(actionItems(registry, 'slash'))

  // The palette also finds the open room's sessions, runs and PRs. The runs
  // list loads only for the Runs view, so an open palette loads it too.
  let targets = $derived(showPalette && roomId ? paletteTargets(hierarchy.filter((m): m is Member & { id: string } => !!m.id), runs?.runs ?? [], (changes ?? []).filter((c): c is ChangeRow & { changeId: string } => !!c.changeId)) : [])
  $effect(() => { if (showPalette && roomId && runs === null) untrack(loadRuns) })
  function openPaletteTarget(t: PaletteTarget) {
    if (t.kind === 'session') openSession(t.session)
    else if (t.kind === 'run') showRun(t.run)
    else openPR(t.pr)
  }

  // A room chosen in the palette. A room just made is not in the list yet.
  async function paletteRoom(id: string) {
    await loadRooms()
    selectRoom(id)
  }

  // openFirst picks the room to show at start: a link's room, else the room
  // the owner had open, else the first active room. The saved room opens
  // beside the room list, not after it.
  const linked = () => new URLSearchParams(location.hash.slice(1)).get('room')
  async function openFirst() {
    const saved = lastRoom()
    const listed = loadRooms()
    if (saved && !linked() && (saved.room === '' || ROOM.test(saved.room))) {
      const opened = selectRoom(saved.room, saved.view)
      started = true
      await listed
      if (!saved.room || rooms.length === 0 || rooms.some((r) => r.id === saved.room)) return
      // The saved room is gone.
      await opened
      loadError = ''
    } else {
      await listed
      if (hashTarget()) {
        started = true
        return
      }
    }
    const first = rooms.find((r) => !r.archived)
    if (first) selectRoom(first.id)
    started = true
  }

  // start runs once the app may talk to the server (after Connect's landing
  // page, if there was one). It returns the cleanup.
  function start() {
    const mq = window.matchMedia(NARROW)
    const resize = () => {
      narrow = mq.matches
      navOpen = false
      if (narrow) {
        showMembers = false
        showPins = false
      }
    }
    mq.addEventListener('change', resize)
    api.dataDir().then((d: string) => (dataDir = d))
    api.health().then((h: { status?: string } | null) => (health = h?.status ?? ''))
    loadProfiles()
    // Check, never install: `dyna update` stays an explicit step.
    api.updateNotice().then((n: UpdateNotice | null) => (update = n?.text ? n : null)).catch(() => {})
    openFirst()
    const link = watch((r) => (route = r))
    // The call widget lives outside every view; it takes the same events.
    const engine = engineWatch((s) => (engineStatus = s), browserEngine(engineCheck))
    const off = (onEvent as (event: (ev: StreamEvent) => void, state: (up: boolean) => void) => () => void)((ev) => { handle(ev); voiceWidget?.event(ev) }, (up) => { link.streamState(up); engine.state(up) })
    registerWorker()
    const fromWorker = (e: MessageEvent) => {
      if (e.data?.type !== 'open') return
      callPush = callLink(e.data.room, e.data.call ?? null) ?? callPush
      openTarget(e.data.room, e.data.post)
    }
    navigator.serviceWorker?.addEventListener('message', fromWorker)
    window.addEventListener('hashchange', hashChanged)
    const clicks = (e: MouseEvent) => {
      const a = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>('a[href]') : null
      if (!a) return
      e.preventDefault()
      const target = parseDetailHref(a.getAttribute('href'))
      if (target) {
        openDetail(target)
        return
      }
      if (/^https?:/.test(a.href)) openExternal(a.href)
    }
    document.addEventListener('click', clicks)
    const ticker = setInterval(() => (rooms = [...rooms]), 30000) // keep "ago" labels fresh
    sessionData.start()
    workflowData.start()
    return () => {
      mq.removeEventListener('change', resize)
      off?.()
      link.stop()
      engine.stop()
      navigator.serviceWorker?.removeEventListener('message', fromWorker)
      window.removeEventListener('hashchange', hashChanged)
      document.removeEventListener('click', clicks)
      clearInterval(ticker)
    }
  }

  onMount(() => {
    // One viewer at a time: a pop-out takes the stream, so the tab goes back to Messages.
    const popped = () => { if (sessionTab === 'computer') sessionTab = 'chat' }
    window.addEventListener('dyna:computer-popped', popped)
    // A hook card opens its run's log in Settings.
    const hookLog = (event: Event) => {
      const target = hookLogFrom(event)
      if (target) openHookLog(target)
    }
    window.addEventListener(HOOK_LOG_EVENT, hookLog)
    const visibility = () => {
      pageVisible = document.visibilityState === 'visible'
      if (pageVisible && started) refreshVisibleRoom()
    }
    document.addEventListener('visibilitychange', visibility)
    // Changed computer events keep badges current; poll only an open desktop view.
    // One status read a minute, every 5 seconds while an operation runs.
    let syncAt = 0
    const syncPoll = setInterval(() => {
      if (needPair || !pageVisible || !roomId || Date.now() - syncAt < syncDelay(syncRepo?.operation)) return
      syncAt = Date.now()
      loadSync()
    }, 1000)
    const computerPoll = setInterval(() => { if (!needPair && pageVisible && (drill || view === 'computers')) loadComputers() }, 10000)
    let stopApp = () => {}
    let gone = false
    if (!needPair) boot().then((h) => {
      if (gone || needPair) return
      if (h) halt = h
      else stopApp = start()
    })
    const transferPoll = setInterval(() => {
      if (needPair || !pageVisible) return
      for (const controller of bulkControllers.values()) controller.refreshTransfers()
    }, 5000)
    // A refused session opens login and stops all background work.
    // Unmount uses the same cleanup, including a pending boot.
    const stop = () => {
      if (gone) return
      gone = true
      window.removeEventListener('dyna:computer-popped', popped)
      window.removeEventListener(HOOK_LOG_EVENT, hookLog)
      document.removeEventListener('visibilitychange', visibility)
      clearInterval(computerPoll)
      clearInterval(syncPoll)
      roomSessionController?.destroy()
      roomControlsVisit++
      for (const controller of bulkControllers.values()) controller.destroy()
      consolidationAlive = false
      clearInterval(transferPoll)
      roomData.destroy()
      sessionData.destroy()
      workflowData.destroy()
      stopApp()
    }
    onUnauthorized(() => { needPair = true; stop() })
    return stop
  })
</script>

<svelte:window onkeydown={shortcut} onresize={() => (winWidth = window.innerWidth)} />

{#if halt}
<ConnectStop key={halt.key} error={halt.error} />
{:else if needPair}
<Pair />
{:else if pop}
<ComputerPopout room={pop.room} session={pop.session} />
{:else}
<div class="app" class:narrow>
  <!-- On a wide screen this wrapper adds no box; on a narrow one it is the drawer. -->
  <div class="nav" class:open={navOpen}>
    <RoomRail {rooms} selected={machinesNav ? '' : roomId} onselect={selectRoom} onmenu={(r: Room, x: number, y: number, anchor?: HTMLElement) => (roomMenu = { r, x, y, anchor })} />
    {#if showSide || narrow}
      <Sidebar machines={machines} machinesOpen={machinesNav} machineId={machineId} onmachine={openMachine} selected={drill?.address ?? ''} onroom={(id: string) => selectRoom(id)} {computers} channelUnread={feed?.channelUnread ?? {}} room={current ?? room} prs={openPRMap} onroommenu={(x: number, y: number, anchor?: HTMLElement) => { const r = current ?? room; if (r) roomMenu = { r, x, y, anchor } }} view={view as typeof GENERAL} questions={questions.filter((q) => q.state === 'open' && q.to === 'owner').length} {live} {runs} {dataDir} {ownerHue} {route} update={!!update} onsettings={() => openSettings()} onlogout={signOut} onview={openView} onmember={openAgent} />
    {/if}
  </div>
  {#if narrow && navOpen}
    <button class="scrim" aria-label="Close the room list" onclick={() => (navOpen = false)}></button>
  {/if}
  <main class="main">
    <header class="bar" class:dm-bar={!!drill} inert={narrow && reviewOpen}>
      {#if narrow}
        <button class="icon" onclick={() => (navOpen = true)} title="Rooms and views" aria-label="Open the room list" aria-expanded={navOpen}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
        </button>
      {:else}
      <button class="icon" onclick={() => (showSide = !showSide)} title={showSide ? 'Hide sidebar (Ctrl+B)' : 'Show sidebar (Ctrl+B)'} aria-label={showSide ? 'Hide sidebar' : 'Show sidebar'} aria-expanded={showSide}>
        <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><rect x="3.5" y="4.5" width="17" height="15" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M9.5 4.5v15" stroke="currentColor" stroke-width="1.8" />{#if showSide}<path d="M5.8 8.5h1.6M5.8 11.5h1.6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />{/if}</svg>
      </button>
      {/if}
      {#if !roomId}
        <span class="title">{machineDetail ? machineDetail.name : 'Machines'}</span>
        {#if !machineId}<span class="meta">{machinesSub(machines)}</span>{/if}
      {:else if room}
        {#if drill}
          {#if readOnlyDm}
            <button class="dm-back" aria-label="Back to Services" title="Back to Services" onclick={() => (drill = null)}><span class="back-arrow" aria-hidden="true">‹</span><span class="back-name">Services</span></button>
            <span class="dm-sep" aria-hidden="true">/</span>
            <span class="title">{drill.name}</span>
          {:else}
          <button class="dm-back" aria-label={`Back to #${roomLabel(current ?? room)}`} title={`Back to #${roomLabel(current ?? room)}`} onclick={() => (drill = null)}><span class="back-arrow" aria-hidden="true">‹</span><span class="back-name">#{roomLabel(current ?? room)}</span></button>
          {#if sessionTrail.length}
            <nav class="trail" aria-label="Parent sessions">
              {#each sessionTrail as m (m.id)}
                <span class="dm-sep" aria-hidden="true">/</span>
                <button class="dm-back dm-crumb" title={`Open ${m.name}`} onclick={() => openSession(m)}><span class="back-name">{m.name}</span></button>
              {/each}
            </nav>
          {/if}
          <span class="dm-sep" aria-hidden="true">/</span>
          <DmIdentity member={session?.member || drill} usage={session?.usage} onopen={openMember} />
          {#if !narrow}<div class="dm-tabs"><SessionTabs bind:tab={sessionTab} media={session?.media ?? []} computer={computers && !drill.rootSeat ? computerOf(drill.id) ?? { state: 'none' } : null} shells={drillShells.length} /></div>{/if}
          <span class="spacer"></span>
          <UseIcons shells={drillShells.length} computer={inUse(drillComputer)} tab={sessionTab} onselect={(tab) => (sessionTab = tab)} />
          {#if isRootDm}<CallButton {room} />{/if}
          <button class="dm-threads threads" class:on={threadsOpen} aria-expanded={threadsOpen} aria-label="Threads" title="Threads" onclick={() => { threadsOpen = !threadsOpen; selectedThread = ''; if (threadsOpen) sessionTab = 'chat' }}><svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h9A1.5 1.5 0 0 1 16 5.5v6a1.5 1.5 0 0 1-1.5 1.5H9l-3.5 3v-3h0A1.5 1.5 0 0 1 4 11.5z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg><b>{threadCount}</b></button>
          {/if}
        {:else if view === 'runs' && openRun}
          <button class="crumb" onclick={() => (openRun = '')}><span class="hash">#</span>runs</button>
          <span class="trail">
            {#each runTrail as m (m.id)}
              <span class="slash" aria-hidden="true">/</span>
              <button class="crumb" title={`Open ${m.name}`} onclick={() => openSession(m)}>{m.name}</button>
            {/each}
          </span>
          <span class="slash" aria-hidden="true">/</span>
          <span class="title">{runDetail?.run?.name ?? openRun}</span>
        {:else if workRow}
          <span class="title" title={room.repo}><span class="hash">#</span><span class="name">{workRow.label}</span></span>
          {#if workTabs && !narrow}<div class="dm-tabs"><Tabs tabs={workTabs} tab={view} label={workRow?.label ?? ''} onselect={openView} /></div>{/if}
        {:else}
          <span class="title" title={room.repo}><span class="name">{roomLabel(current ?? room)}</span></span>
        {/if}
        {#if !drill}
        <span class="spacer"></span>
        <CallButton {room} />
        <span class="meta">{Math.max(0, people.length - 1)} session{people.length === 2 ? '' : 's'}{#if live.length}&nbsp;·&nbsp;<em>{live.length} working</em>{/if}</span>
        {#if syncRemotes.length}<SyncBadge remotes={syncRemotes} status={syncRepo} note={syncNote} onrefresh={() => syncAction(api.syncRefresh)} onexport={() => syncAction(api.gitExport)} />{/if}
        {#if !narrow}
          <button class="icon" onclick={() => openSearch()} title="Search (Ctrl+K)" aria-label="Search sessions and messages"><SearchIcon /></button>
        {/if}
        <button class="icon pin" class:on={showPins} onclick={() => { showPins = !showPins; if (showPins) showMembers = false }} title={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-label={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-expanded={showPins}>
          <PinIcon size={21} />
          {#if pins.length}<span class="badge">{pins.length}</span>{/if}
        </button>
        {/if}
      {/if}
      {#if room?.repo && !readOnlyDm}<button class="icon" class:on={reviewOpen} aria-controls="review-workspace" aria-expanded={reviewOpen} title={reviewOpen ? 'Hide review' : 'Review files, changes, PRs and issues'} aria-label={reviewOpen ? 'Hide review' : 'Show review'} onclick={() => reviewOpen ? closeReview() : openReview()}>
        <ReviewIcon size={22} />
      </button>{/if}
      {#if !roomId}<span class="spacer"></span>{/if}
      {#if narrow}
        <button class="icon" onclick={() => openSearch()} title="Search (Ctrl+K)" aria-label="Search sessions and messages"><SearchIcon size={22} /></button>
      {/if}
      <!-- Members closes the pinned panel, so the two panels never share the
           side area. The button is the bar's last one, on a phone and a desktop. -->
      {#if room && !drill}
        <button class="icon" class:on={showMembers} onclick={() => { showMembers = !showMembers; if (showMembers) showPins = false }} title={showMembers ? 'Hide member list' : 'Show member list'} aria-label={showMembers ? 'Hide member list' : 'Show member list'} aria-expanded={showMembers}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><circle cx="9" cy="8" r="3.2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><circle cx="16.5" cy="9" r="2.4" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M16 13.6c2.4 0 4 1.5 4.5 4.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
        </button>
      {/if}
    </header>
    <div class="content">
      <div class="conversation-workspace">
      {#key viewIdentity({ roomId, view, drill })}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="center" inert={narrow && reviewOpen} in:fade={{ duration: prView ? 0 : motion(FAST) }} ondragover={dragOver} ondragleave={(e) => { if (!e.currentTarget.contains(e.relatedTarget instanceof Node ? e.relatedTarget : null)) dropping = false }} ondrop={drop}>
        {#if dropping}
          <div class="dropzone"><div><strong>Drop to attach</strong><span>to your next post in {drill ? '@' + drill.name : '#' + roomLabel(current ?? room)}</span></div></div>
        {/if}
        {#if engineStatus}
          <p class="engine-down" class:waiting={engineStatus === 'reconnecting'} role={engineStatus === 'reconnecting' ? 'status' : 'alert'}><strong>Reconnecting…</strong>
            {#if engineStatus === 'reconnecting'}Contact with the engine broke. Dyna tries again now, and posts wait until it answers.
            {:else if engineStatus === 'offline'}This device is offline. Dyna reconnects when its network is back.
            {:else if engineStatus === 'unreachable'}Dyna cannot reach the engine. Check this device's network connection.
            {:else if engineStatus === 'engine'}The engine did not answer. Agents, posts and merges wait until it is back. On the engine's computer, run <code>systemctl --user start dyna-engine</code>.
            {/if}</p>
        {/if}
        {#if health}
          <p class="notice">Posting is off: {health}</p>
        {/if}
        {#if update && !updateHidden && roomId}
          <p class="update">{update.text} <button onclick={() => (updateHidden = true)}>Dismiss</button></p>
        {/if}
        {#if loadError}
          <p class="load-err">{loadError}</p>
        {/if}
        {#if !roomId && machineId}
          <MachinePage machine={machineDetail} error={machineError} busy={machineBusy} probing={machineProbing} onback={closeMachine} onagent={openAgent} onsave={saveMachine} onprobe={probeMachine} />
        {:else if !roomId}
          <Home bind:this={home} {rooms} {update} onroom={selectRoom} onaccount={(id: string) => openSettings('usage', false, '', id)} />
        {:else if !room}
          <Skeleton label="Loading room" />
        {:else if drill}
          <!-- Keyed: staged files and the typed draft belong to one session. -->
          {#if narrow && !readOnlyDm}<SessionTabs bind:tab={sessionTab} media={session?.media ?? []} computer={computers && !drill.rootSeat ? computerOf(drill.id) ?? { state: 'none' } : null} shells={drillShells.length} />{/if}
          {#key drill.id}
            {#if sessionTab === 'computer' && computers}
              <ComputerPane c={computerOf(drill.id) ?? { session: drill.id, name: drill.name, state: 'none', apps: [], stream: {}, viewers: 0, rssKiB: 0, streamRssKiB: 0 }} room={room.id} feed={computerFeed[drill.id] ?? []} chat={{ computerCount: 1, draft: drafts['computer:' + roomId + ':' + drill.id] ?? '', ondraft: computerDraft, session, room, members: hierarchy, onsend: sendToSession, onopenfile: (a: Attachment) => (viewing = a), onpost: jumpTo, onmessages: () => (sessionTab = 'chat') }} />
            {:else if sessionTab === 'shell'}
              <ShellGrid jobs={drillShells} onjob={(job) => { openJob = job.id; drill = null; view = 'jobs' }} />
            {:else if sessionTab === 'media'}
              <SessionMedia onpost={jumpTo} {room} posts={session?.media ?? []} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
            {:else}
              <div class="sess-wrap" style:--shift={threadsOpen && !narrow ? '360px' : '0px'}>
              <Session readOnly={readOnlyDm} bind:readingHistory={sessionReadingHistory} onissue={openIssues} onreopen={reopenSession} {reopening} ondismisssession={dismissSession} {dismissing} onpost={jumpTo} bind:this={sessionView} bind:threadsOpen bind:selectedThread bind:draft={sessionDraft.value} members={hierarchy} jobs={jobs ?? []} onchild={openSession} onjob={(job) => { openJob = job.id; drill = null; view = 'jobs' }} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} {session} error={sessionError} {room} {queue} focusedTurn={sessionTurn} onrecent={() => openSession(drill)} onearlier={loadEarlierSession} onnewer={loadNewerSession} onpresent={presentSession} onunqueue={unqueue} ondismiss={dismissPendingPost} onharness={(name) => openSettings('profiles', false, name)} onsend={sendToSession} />
              {#if showPreview}<ComputerPreview c={drillComputer} session={drill.id} name={drill.name} onopen={() => (sessionTab = 'computer')} />{/if}
              </div>
            {/if}
          {/key}
        {:else}
        {#if workTabs && narrow}<Tabs tabs={workTabs} tab={view} label={workRow?.label ?? ''} onselect={openView} />{/if}
        {#if view === 'library'}
          {#key room.id}<Library roomId={room.id} {narrow} onopen={({ path, dir }) => { openView('files'); if (dir) revealFolder = { path }; else reveal = path }} />{/key}
        {:else if view === 'terminal' && hasTerminal}
          {#key room.id}<Terminal roomId={room.id} ref={room.id} noRepo={!room.repo} {narrow} names={Object.fromEntries(members.filter((m) => m.id).map((m) => [m.id, m.name]))} />{/key}
        {:else if view === 'questions'}
          {#key room.id}<Questions roomId={room.id} {questions} focusId={questionFocus} focusRequest={questionFocusRequest} onanswer={answerQuestion} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />{/key}
        {:else if view === 'services'}
          {#key room.id}<Services roomId={room.id} repo={room.repo ?? ''} members={hierarchy} changes={changes ?? []} onopen={(m: Member) => openSession(m, 0, null, true)} onpr={openPR} />{/key}
        {:else if view === 'visuals'}
          {#key room.id}<Visuals roomId={room.id} {visuals} onopen={(v: Visual) => (viewing = { ...v.attachment, postId: v.post.id })} />{/key}
        {:else if view === 'computers' && computers}
          {#key room.id}<Computers list={computers} error={computerError} onopen={(c: ComputerInfo) => { const m = hierarchy.find((x) => x.id === c.session); if (m) { openSession(m); sessionTab = 'computer' } else popOut(room.id, c.session) }} />{/key}
        {:else if view === 'jobs'}
          {#key room.id}<Jobs older={listPages.jobs?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['jobs'])} {jobs} error={jobsError} jobId={openJob} {narrow} onstop={stopJob} />{/key}
        {:else if view === 'goals'}
          {#key room.id}<Goals roomId={room.id} older={listPages.goals?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['goals'])} {goals} members={hierarchy} selected={goalId} onselect={(id: string) => (goalId = id)} ongoal={openGoal} onexpedite={(g: Goal) => expediteGoal(g).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) }))} />{/key}
        {:else if view === 'runs'}
          {#if openRun}
            {#key openRun}<Run detail={runDetail} error={runError} oncancel={(run) => (confirm = { kind: 'cancelrun', run })} onpause={pauseRun} onsteer={steerAgent} />{/key}
          {:else}
            <Runs {room} list={runs} error={runsError} onopen={showRun} onstart={() => (starting = true)} />
          {/if}
        {:else}
          <Feed {goals} bind:this={feedView} bind:stuck={feedAtBottom} label={channelLabel} {room} posts={shownPosts} onreveal={revealPost} {questions} calls={view === GENERAL ? roomCalls : []} onacceptcall={acceptCall} ondeclinecall={declineCall} {view} earlier={feed?.earlier ?? 0} onearlier={(near: () => boolean) => loadEarlier('older', '', near)} newer={feed?.newer ?? 0} onnewer={(near: () => boolean) => loadEarlier('newer', '', near)} onpresent={() => loadEarlier('present')} onaround={(id: string) => loadEarlier('around', id)} {unreadFrom} pinned={pinnedIds} onaddress={openAddress} onreply={startReply} onforward={(p: Post) => composer?.forward(p)} onpin={togglePin} ondetail={openDetail} onquestion={openQuestion} ongoal={openGoal} onissue={openIssues} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
          <!-- Keyed: staged files belong to one room and never follow a switch. -->
          {#key room.id + view}
            <Composer onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} bind:this={composer} label={channelLabel} prepareText={(text: string) => prepareChannelText(text, channel ?? null, channels, (value) => api.preview(room.id, value))} {room} {choices} {profiles} commands={composerCommands} onslash={() => { loadProfiles(); loadTemplates() }} {reply} bind:draft={draft.value} onsend={send} oncancelreply={() => (reply = null)} />
          {/key}
        {/if}
        {/if}
      </div>
      {/key}
      {#if room && reviewOpen}
        {#key room.id + ':' + (drill?.rootSeat ? '' : drill?.id ?? '')}
          <ReviewWorkspace roomId={room.id} session={drill?.rootSeat ? '' : drill?.id ?? ''} sessionName={drill?.name ?? ''} members={hierarchy} bind:tab={reviewTab} {reveal} folder={revealFolder} version={reviewVersion} onpr={openPR} onclose={closeReview} onselect={(id) => { const member = hierarchy.find((m) => m.id === id); if (member) openSession(member); else { drill = null; view = GENERAL } }}>
            {#snippet prs()}
              <GitHub {mergeVersion} older={listPages.changes?.older} loadingOlder={olderLoading} failedOlder={olderFailed} onolder={() => loadOlderLists(['changes'])} roomId={room.id} kind="pr" active={reviewTab === 'prs'} session={routeSession(drill)} link={prLink} {changes} members={hierarchy} onmention={mentionReference} onsession={(m) => openSession(m)} onpr={openPR} ontrack={trackPR} onuntrack={untrackPR} onresume={resumePR} {reviewer} onreviewer={loadReviewer} goals={knownGoals} onexpedite={expediteGoal} onlinkgoal={linkPRGoal} />
            {/snippet}
            {#snippet issuesTab()}
              <GitHub {mergeVersion} older={listPages.changes?.older} loadingOlder={olderLoading} failedOlder={olderFailed} onolder={() => loadOlderLists(['changes'])} roomId={room.id} kind="issue" active={reviewTab === 'issues'} localIssues={[...issues, ...issueHistory]} oncloseissue={askCloseIssue} onexportissue={(i) => triageIssue(() => api.exportIssue(roomId, i.id))} ondismississue={(i) => triageIssue(() => api.closeIssue(roomId, i.id, 'Dismissed'))} integration={syncRepo?.integration as import('./lib/api/lanesync.ts').Integration | null} link={prLink} {changes} members={hierarchy} onmention={mentionReference} onsession={(m, tab) => { openSession(m); if (tab === 'prs') openReview('prs'); else if (tab) sessionTab = tab }} onpr={openPR} ontrack={trackPR} onuntrack={untrackPR} onresume={resumePR} {reviewer} onreviewer={loadReviewer} goals={knownGoals} onexpedite={expediteGoal} onlinkgoal={linkPRGoal} />
            {/snippet}
          </ReviewWorkspace>
        {/key}
      {/if}
      </div>
      {#if narrow && roomId && room && (showPins || (showMembers && !drill) || (view === 'goals' && selectedGoal))}
        <button class="scrim" aria-label="Close the panel" onclick={() => { showPins = false; showMembers = false; goalId = '' }}></button>
      {/if}
      <!-- On a wide screen this wrapper adds no box; on a narrow one it is a sheet. -->
      <div class="panel">
      {#if roomId && room && view === 'goals' && selectedGoal}
        {#key selectedGoal.id}<GoalDetail roomId={room.id} goal={selectedGoal} members={hierarchy} onchanged={loadGoals} onclose={() => (goalId = '')} onexpedite={expediteGoal} />{/key}
      {:else if roomId && room && showPins}
        <Pins {room} {pins} ondetail={openDetail} onjump={(id: string) => { if (narrow) showPins = false; jumpTo(id) }} onunpin={togglePin} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} onclose={() => (showPins = false)} />
      {:else if roomId && room && showMembers && !drill}
        <Members computers={computers ?? []} onopencomputer={(m: Member) => { openSession(m); sessionTab = 'computer' }} members={people} focus={view.startsWith('channel:') ? view.slice('channel:'.length) : ''} scope="{room.id}|{view}" {roomControls} onroomactions={roomSessionActions} onresumeroom={() => roomSessionController?.run('resume')} onreloadcontrols={() => roomSessionController?.load()} oncleardormant={askClearDormant} ondismiss={(m: Member) => dismissSession(m).catch((e) => say({ tone: 'error', subject: `Could not dismiss ${m.name}`, text: errorText(e) }))} {dismissing} ontakeseat={takeBackSeat} {fresh} selecting={selectingSessions} picked={pickedSessions} onselection={toggleSessionSelection} onpick={pickSession} onactions={selectionActions} selected={''} closed={showClosed} onselect={openMember} onmenu={(m: Member, x: number, y: number, anchor?: HTMLElement) => (menu = { m, x, y, anchor })} onclosed={() => { showClosed = !showClosed; loadMembers() }} />
      {/if}
      </div>
    </div>
  </main>
  <VoiceWidget bind:this={voiceWidget} bind:link={callPush} {narrow} {rooms} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
</div>
{/if}
{#if showPalette}
  <Palette {rooms} commands={paletteCommands} {targets} {roomId} roomName={room?.name ?? ''} initial={searchText} onmessage={jumpTo} path={palettePath} onroom={paletteRoom} ontarget={openPaletteTarget} onclose={closeSearch} />
{/if}
{#if viewing && room}
  {@const attachment = viewing}
  <Viewer roomId={viewing.roomId ?? room.id} att={viewing} onjump={attachment.postId && (attachment.roomId ?? room.id) === room.id ? () => { viewing = null; if (attachment.postId) jumpTo(attachment.postId) } : null} onclose={() => (viewing = null)} />
{/if}
{#if showSettings}
  {#key settingsKey}
    <Settings {update} {dataDir} start={settingsAt.start} pairNow={settingsAt.pairNow} profile={settingsAt.profile ?? ''} accountId={settingsAt.accountId ?? ''} initialSelection={settingsAt.initialSelection} roomId={room?.id ?? ''} oncreateprofile={createProfile} onclose={() => { showSettings = false; loadProfiles(); home?.refresh(); voiceCall.refresh() }} />
  {/key}
{/if}
{#if showRoomSettings && room}
  {#key roomSettingsKey}
  {#snippet roomRules()}<Rules roomId={room.id} view={rules} members={hierarchy} {ownerHue} bind:editing={rulesEditing} onchanged={loadRules} />{/snippet}
  <RoomSettings {room} start={roomSettingsAt.start} hookRun={roomSettingsAt.hookRun ?? null} {reviewer} onreviewer={loadReviewer} controls={roomControls} onsessions={(action: string) => roomSessionController?.run(action)} onaction={(id: string) => actionRegistry(room).find((a) => a.id === id)?.run()} rules={roomRules} busy={rulesEditing} onclose={() => (showRoomSettings = false)} />
  {/key}
{/if}
{#if roomMenu}
  <Menu x={roomMenu.x} y={roomMenu.y} title={roomLabel(roomMenu.r)} items={roomMenuItems(roomMenu.r)} onclose={() => { const a = roomMenu?.anchor; roomMenu = null; a?.focus?.() }} />
{/if}
{#if roomControlsMenu}
  {@const context = { ...roomControlsMenu }}
  <Menu x={context.x} y={context.y} title="Session controls" items={roomSessionItems(context)} onclose={closeRoomSessionMenu} />
{/if}
{#if clearDormantConfirm}
  {@const context = { ...clearDormantConfirm }}
  <Confirm title="Clear dormant sessions?" lines={[`Close ${context.count} dormant session${context.count === 1 ? '' : 's'}?`, 'Dormant sessions finished with no open work. A session that gained work since then stays open. Transcripts stay available under closed sessions. Clean merged worktrees are removed; unmerged or dirty worktrees are kept and reported.']} action="Clear dormant sessions" onconfirm={() => clearRoomDormant(context)} oncancel={() => (clearDormantConfirm = null)} />
{/if}
{#if selectionMenu}
  {@const context = selectionMenu}
  <Menu x={context.x} y={context.y} title={`${context.ids.length} selected`} items={selectionItems(context)} onclose={closeSelectionMenu} />
{/if}
{#if menu}
  <Menu x={menu.x} y={menu.y} title={menu.m.address} items={menuItems(menu.m)} onclose={closeMenu} />
{/if}
{#if currentBulk?.value && currentBulk.roomId === roomId}
  {@const batch = { ...currentBulk, value: currentBulk.value }}
  {#key batch.key}<BulkSessionAction {batch} members={hierarchy} onclose={() => (activeBulk = 0)} onrun={(force) => runBulk(batch.key, force)} onconfigure={(patch) => bulkControllers.get(batch.key)?.configure(patch)} onreload={() => bulkControllers.get(batch.key)?.load()} onselectfailed={() => selectBulkFailures(batch)} />{/key}
{/if}
{#if consolidating && room && consolidating.roomId === roomId}
  {@const context = { ...consolidating }}
  {#key context.key}<SessionConsolidation roomId={context.roomId} sessions={context.sessions} onoperation={(operation) => consolidationChanged(context, operation)} onclose={closeConsolidation} onopen={(id) => { const m = hierarchy.find((m) => m.id === id); closeConsolidation(); if (m) openSession(m) }} />{/key}
{/if}
{#if transferring && room}
  <ModelTransfer member={transferring} roomId={room.id} {profiles} onclose={() => { transferring = null; loadMembers(); if (drill) loadSession() }} />
{/if}
{#if expediting && room}
  {@const context = { ...expediting }}
  <ExpediteGoal goals={context.goals} roomId={room.id} session={context.member.address} expeditedId={$expedite.goalId} onpick={(g: Goal) => { expediting = null; expediteGoal(g).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) })) }} onstop={() => { expediting = null; stopExpediting(context.goals) }} onclose={() => (expediting = null)} />
{/if}
{#if confirm && confirmText}
  <Confirm {...confirmText} busy={confirm.busy} error={confirm.error} onconfirm={() => runConfirm()} oncancel={() => (confirm = null)} />
{/if}
{#if starting && room}
  <RunWorkflow {room} onstarted={runStarted} oncancel={() => (starting = false)} />
{/if}
{#if ask}
  <Prompt {...ask} onsubmit={ask.submit} oncancel={() => (ask = null)} />
{/if}
{#if closeResult}
  <section class="close-result" role="status" aria-label="Session close result">
    <strong>{closeResult.name} is closed.</strong>
    {#if closeResult.items.length}<ul>{#each closeResult.items as item}<li>{item}</li>{/each}</ul>{/if}
    <button onclick={() => (closeResult = null)}>Dismiss</button>
  </section>
{:else if toast}
  <Toast {toast} detail={toastDetail} detailOpen={toastDetailOpen} ondismiss={hideToast} onaction={runToastAction} ontoggle={toggleToastDetail} />
{/if}
{#if !closeResult && toast && toastDetail && toastDetailOpen}
  <div class="toast-details" role="region" aria-label={toastDetail.title}>
    <header><strong>{toastDetail.title}</strong><button type="button" onclick={closeToastDetail}>Dismiss</button></header>
    <ul>{#each toastDetail.items as item, i (i)}<li class:error={item.error}><strong>{item.name}</strong><span>{item.text}</span></li>{/each}</ul>
  </div>
{/if}
{#if card && room}
  <ProfileModal {goals} older={listPages.goals?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['goals'])} ongoal={(id) => { card = null; openGoal(id) }} onpost={jumpTo} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} {profile} error={profileError} {room} ondetail={openDetail} onclose={() => (card = null)} onmessage={openSession} onmention={mentionMember} />
{/if}

<style>
  .conversation-workspace { flex: 1; min-height: 0; min-width: 0; display: flex; position: relative; }
  .sess-wrap { position: relative; flex: 1; min-height: 0; min-width: 0; display: flex; flex-direction: column; }
  .app { display: flex; height: 100vh; height: 100dvh; min-height: 0; }
  .nav, .panel { display: contents; }
  .scrim { display: none; }
  .main { flex: 1; display: flex; flex-direction: column; min-width: 0; background: var(--main); }
  .bar {
    height: 64px;
    flex: none;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 16px 0 10px;
    border-bottom: 1px solid #1a191c;
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.18);
    min-width: 0;
  }
  .dm-bar { height: 56px; gap: 8px; }
  /* A work view gives its whole width to the tab labels: the room's repo path
     is in the title's tooltip, because a clipped tab is worse than a clipped
     path. Under 760 px the tabs scroll instead. */
  .dm-tabs { min-width: 0; }
  .dm-tabs :global(.tabs) { border: 0; padding: 0; }
  .dm-tabs :global(button) { padding: 5px 10px; font-size: 12.5px; white-space: nowrap; border-radius: 6px; }
  .dm-tabs :global(button.on) { background: var(--raise); }
  .dm-tabs :global(button.on::after) { display: none; }
  .dm-threads, .dm-back { flex: none; color: var(--muted); font-size: 12.5px; padding: 6px 8px; border-radius: 6px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .dm-back { display: inline-flex; align-items: center; gap: 5px; max-width: 160px; }
  .back-arrow { font-size: 17px; line-height: 1; }
  .back-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dm-sep { color: var(--line); margin: 0 -2px; }
  .dm-threads { display: inline-flex; align-items: center; gap: 6px; }
  .dm-threads.on { color: var(--ink); background: var(--raise); }
  .dm-threads b { border-radius: 8px; color: var(--side); background: var(--iris); font-size: 11px; padding: 0 5px; }
  @media (hover: hover) {
    .dm-back:hover { color: var(--ink); background: var(--hover); }
  }
  .dm-back:active { background: var(--select); }
  .title { font-weight: 600; font-size: 16px; display: inline-flex; align-items: center; white-space: nowrap; min-width: 0; }
  .title .name { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .hash { color: var(--faint); font-size: 22px; font-weight: 400; margin-right: 6px; line-height: 1; }
  .crumb { display: inline-flex; align-items: center; font-weight: 600; color: var(--muted); font-size: 16px; transition: color var(--motion-fast) var(--ease); }
  /* The channel trail is one flex item, so a long trail shrinks as a unit. */
  .trail { display: flex; align-items: center; gap: 10px; min-width: 0; }
  @media (hover: hover) {
    .crumb:hover { color: var(--ink); }
  }
  .slash { color: var(--faint); }
  .trail { display: contents; }
  .dm-crumb { max-width: 140px; }
  .who { display: inline-flex; align-items: center; gap: 8px; }
  .chip { font-size: 12px; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 1px 7px; }
  .spacer { flex: 1; }
  .meta { color: var(--faint); font-size: 13px; white-space: nowrap; }
  .meta em { font-style: normal; color: var(--gold); }
  .icon { color: var(--muted); display: grid; place-items: center; width: 32px; height: 32px; border-radius: 6px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .icon:hover { color: var(--ink); background: var(--hover); }
  }
  .icon:active { background: var(--select); }
  .icon.on { color: var(--ink); }
  .icon.pin { position: relative; }
  .badge {
    position: absolute;
    top: 0;
    right: -2px;
    min-width: 15px;
    height: 15px;
    padding: 0 4px;
    border-radius: 8px;
    background: var(--select);
    color: var(--ink);
    font-size: 10.5px;
    font-weight: 600;
    line-height: 15px;
    text-align: center;
    border: 1px solid var(--line);
  }
  .content { flex: 1; display: flex; min-height: 0; }
  .center { flex: 1; display: flex; flex-direction: column; min-width: 0; position: relative; }
  .dropzone {
    position: absolute;
    inset: 12px;
    z-index: 20;
    border: 2px dashed var(--iris-edge);
    border-radius: 12px;
    background: rgba(28, 27, 30, 0.88);
    display: grid;
    place-items: center;
    pointer-events: none;
  }
  .dropzone div { display: flex; flex-direction: column; align-items: center; gap: 4px; }
  .dropzone strong { font-size: 18px; font-weight: 600; }
  .dropzone span { color: var(--muted); font-size: 14px; }
  .notice { color: var(--gold); padding: 8px 16px; margin: 0; font-size: 13px; border-bottom: 1px solid var(--line-soft); }
  .load-err { color: var(--coral); padding: 8px 16px; margin: 0; font-size: 13px; }
  .engine-down { color: var(--coral); padding: 8px 16px; margin: 0; font-size: 13px; border-bottom: 1px solid var(--line-soft); }
  .engine-down.waiting { color: var(--muted); }
  .engine-down strong { font-weight: 600; margin-right: 4px; }
  .update { color: var(--muted); padding: 7px 16px; margin: 0; font-size: 12.5px; border-bottom: 1px solid var(--line-soft); background: var(--gold-wash); }
  .update button { color: var(--faint); font-size: 12px; margin-left: 8px; text-decoration: underline; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .update button:hover { color: var(--ink); }
  }
  /* A close result is its own floating panel. The toast component's styles are
     scoped to it, so this panel carries the position and surface it needs. */
  .close-result {
    position: fixed;
    left: 50%;
    bottom: 84px;
    transform: translateX(-50%);
    z-index: 70;
    width: max-content;
    max-width: min(640px, calc(100vw - 32px));
    max-height: 50vh;
    overflow: auto;
    overflow-wrap: anywhere;
    padding: 10px 14px;
    border: 1px solid var(--line-soft);
    border-radius: var(--r-sm);
    background: #121113;
    color: var(--ink-2);
    font-size: var(--t-ui);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
  }
  .close-result ul { margin: 8px 0; padding-left: 20px; }
  .close-result button { margin-top: 8px; }
  .toast-details {
    position: fixed;
    left: 50%;
    bottom: 132px;
    transform: translateX(-50%);
    z-index: 71;
    width: min(560px, calc(100vw - 32px));
    max-height: 50vh;
    overflow: auto;
    overflow-wrap: anywhere;
    background: #121113;
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
    padding: 10px 12px;
    font-size: 13px;
    color: var(--muted);
  }
  .toast-details header { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 6px; }
  .toast-details header strong { color: var(--ink-2); font-weight: 600; }
  .toast-details ul { margin: 0; padding: 0; list-style: none; }
  .toast-details li { display: grid; gap: 2px; margin: 6px 0; }
  .toast-details li strong { color: var(--ink-2); font-weight: 500; }
  .toast-details li.error span { color: var(--coral); }

  /* A phone: one column. The feed takes the width; the room rail and the
     sidebar slide in as a drawer from the left, and the members or pins
     panel as a sheet from the right. Touch targets are at least 44 px. */
  @media (max-width: 760px) {
    .app { padding-top: env(safe-area-inset-top); }
    .nav {
      display: flex;
      position: fixed;
      z-index: 50;
      top: 0;
      bottom: 0;
      left: 0;
      max-width: calc(100vw - 48px);
      padding-top: env(safe-area-inset-top);
      padding-bottom: env(safe-area-inset-bottom);
      background: var(--side);
      transform: translateX(-105%);
      transition: transform var(--motion) ease-out;
      box-shadow: 4px 0 24px rgba(0, 0, 0, 0.45);
    }
    .nav.open { transform: none; }
    .nav > :global(.side) { width: min(240px, calc(100vw - 120px)); }
    .panel:has(> :global(*)) {
      display: flex;
      position: fixed;
      z-index: 45;
      top: 0;
      bottom: 0;
      right: 0;
      width: min(320px, 88vw);
      padding-top: env(safe-area-inset-top);
      padding-bottom: env(safe-area-inset-bottom);
      background: var(--side);
      box-shadow: -4px 0 24px rgba(0, 0, 0, 0.45);
    }
    .panel:has(> :global(.detail)) { width: 100vw; }
    .panel > :global(*) { width: 100% !important; flex: 1; }
    .scrim { display: block; position: fixed; inset: 0; z-index: 40; background: rgba(0, 0, 0, 0.5); }
    .bar { height: 52px; padding: 0 6px 0 4px; gap: 4px; }
    .meta, .slash, .chip { display: none; }
    .dm-bar .back-name, .dm-bar .back-arrow, .dm-bar .dm-sep { display: none; }
    .dm-bar > .spacer { display: none; }
    .dm-bar .dm-back { width: 36px; max-width: none; font-size: 0; padding: 6px; }
    .dm-bar .dm-back::before { content: '←'; font-size: 18px; }
    .dm-threads, .dm-back { min-height: 44px; }
    .dm-bar > .icon:last-child { display: none; }
    /* Keep the page title readable while the bar buttons keep their width.
       The room bar now holds Search and Members too, so the title gives way
       first and ends in an ellipsis on the narrowest phones. */
    .title, .crumb { min-width: 40px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .trail { gap: 4px; }
    .crumb { flex: 0 1 auto; max-width: 40%; }
    /* A phone shows only the nearest parent of the breadcrumb. In a session,
       its arrow replaces the room arrow and goes up one level. */
    .trail > :not(:last-child) { display: none; }
    .dm-bar .dm-back:has(+ .trail) { display: none; }
    .dm-bar .trail .dm-crumb { width: auto; flex: 0 1 auto; min-width: 0; max-width: 26vw; font-size: 12.5px; padding: 6px; gap: 4px; }
    .dm-bar .trail .dm-crumb::before { flex: none; }
    .dm-bar .trail .back-name { display: inline; }
    .who { min-width: 0; }
    .icon { width: 44px; height: 44px; flex: none; }
    .center { padding-bottom: env(safe-area-inset-bottom); }
    .toast-details { bottom: calc(146px + env(safe-area-inset-bottom)); max-height: 54vh; }
    .close-result { bottom: calc(96px + env(safe-area-inset-bottom)); }
  }
</style>
