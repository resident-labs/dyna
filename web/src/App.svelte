<script lang="ts">
  import { refreshHeldComments } from './lib/prs/heldComments.ts'
  import { createReloadGate, listVisible } from './lib/ui/reloadGate.ts'
  import Skeleton from './lib/ui/Skeleton.svelte'
  import { untrack } from 'svelte'
  import { fade } from 'svelte/transition'
  import { FAST, motion } from './lib/ui/motion.ts'
  import { dialog } from './lib/ui/dialog.ts'
  import { trackViewportHeight } from './lib/ui/viewport.ts'
  import { viewIdentity } from './lib/ui/viewIdentity.ts'
  import { createReadMarker } from './lib/rooms/readMarker.ts'
  import { createRoomActions } from './lib/app/roomActions.svelte.ts'
  import { bindRoomLoader, roomData } from './lib/app/roomLoader.svelte.ts'
  import { bindMachines, machineView, watchMachines } from './lib/app/machines.svelte.ts'
  import { bindGoals, goalView } from './lib/app/goals.svelte.ts'
  import { bindSessionView, sessionView as transcripts } from './lib/app/sessionView.svelte.ts'
  import { bindIssues, issueView } from './lib/app/issues.svelte.ts'
  import { bindPins, pinView } from './lib/app/pins.svelte.ts'
  import { bindPRs, changeView } from './lib/app/prs.svelte.ts'
  import { bindQuestions, questionView } from './lib/app/questions.svelte.ts'
  import { bindComputers, computerView } from './lib/app/computers.svelte.ts'
  import { bindConfirm, confirmView } from './lib/app/confirm.svelte.ts'
  import { bindSelection, selectionView } from './lib/app/selection.svelte.ts'
  import { bindSync, syncView } from './lib/app/sync.svelte.ts'
  import { createSettingsPanel } from './lib/app/settingsPanel.svelte.ts'
  import { closeToastDetail, hideToast, runToastAction, say, sayDetails, sayLine, toasts, toggleToastDetail } from './lib/app/toasts.svelte.ts'
  import { bindLists, listView, applyListPage, applyListItem, loadOlderLists } from './lib/app/lists.svelte.ts'
  import { patchListRows } from './lib/ui/listChanges.ts'
  import { reloadsUnread } from './lib/ui/unreadReload.ts'
  import { createWorkflowData } from './lib/workflows/workflowData.ts'
  import { onMount, tick } from 'svelte'
  import { errorText } from './lib/errorText.ts'
  import { api, onEvent, engineCheck, openExternal, onUnauthorized, logout } from './api.ts'
  import NavPane from './lib/app/shell/NavPane.svelte'
  import PageHeader from './lib/app/shell/PageHeader.svelte'
  import PageNotices from './lib/app/shell/PageNotices.svelte'
  import CallButton from './lib/voice/CallButton.svelte'
  import VoiceHost from './lib/app/shell/VoiceHost.svelte'
  import { voiceCall } from './lib/voice/voiceCall.ts'
  import { callLink, callTarget } from './lib/voice/callRequests.ts'
  import { GENERAL, VOICE, isFeedView, inView, homeView, feedModel, parentTrail, prepareFeedText } from './lib/rooms/feedViews.ts'
  import { seatDMConversation } from './lib/rooms/conversations.ts'
  import { isShellResult, shellJobID } from './lib/rooms/feedRows.ts'
  import { HOOK_LOG_EVENT, hookLogFrom } from './lib/settings/hookLog.ts'
  import { mergeSessionPage } from './lib/sessions/sessionPages.ts'
  import { look, revealed, reveal as revealPost } from './lib/rooms/look.svelte.ts'
  import { visibleTo } from './lib/rooms/look.ts'
  import Feed from './lib/rooms/Feed.svelte'
  import Composer from './lib/rooms/Composer.svelte'
  import Members from './lib/rooms/Members.svelte'
  import Toast from './lib/ui/Toast.svelte'
  import { clipText } from './lib/ui/toast.ts'
  import { createShortcut } from './lib/app/shortcuts.ts'
  import { createPalette } from './lib/app/palette.svelte.ts'
  import { createSessionActions } from './lib/app/sessionActions.svelte.ts'
  import { createRoomControls } from './lib/app/roomControls.svelte.ts'
  import Pins from './lib/rooms/Pins.svelte'
  import SearchIcon from './lib/ui/SearchIcon.svelte'
  import SyncBadge from './lib/lanesync/SyncBadge.svelte'
  import { syncDelay } from './lib/lanesync/sync.ts'
  import Library from './lib/library/Library.svelte'
  import Requests from './lib/requests/Requests.svelte'
  import Visuals from './lib/rooms/Visuals.svelte'
  import Services from './lib/services/Services.svelte'
  import { changesVisuals } from './lib/files/visuals.ts'
  import { ownerRequestCount, questionID } from './lib/requests/requests.ts'
  import { ownerRequests } from './lib/sessions/termRequest.ts'
  import Goals from './lib/goals/Goals.svelte'
  import GoalDetail from './lib/goals/GoalDetail.svelte'
  import { expedite, expediteScope, isExpedited } from './lib/rooms/expedite.ts'
  import ExpediteGoal from './lib/rooms/ExpediteGoal.svelte'
  import Rules from './lib/rooms/Rules.svelte'
  import PinIcon from './lib/ui/PinIcon.svelte'
  import Session from './lib/sessions/Session.svelte'
  import DmIdentity from './lib/sessions/DmIdentity.svelte'
  import Home from './lib/rooms/Home.svelte'
  import ProfileModal from './lib/settings/ProfileModal.svelte'
  import Viewer from './lib/files/Viewer.svelte'
  import Menu from './lib/ui/Menu.svelte'
  import Confirm from './lib/ui/Confirm.svelte'
  import BulkSessionAction from './lib/sessions/BulkSessionAction.svelte'
  import SessionConsolidation from './lib/sessions/SessionConsolidation.svelte'
  import CloseResult from './lib/sessions/CloseResult.svelte'
  import { type CloseRow, type CloseChoice } from './lib/sessions/closeOutcomes.ts'
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
  import { runningShells, sessionShellJobs } from './lib/sessions/shells.ts'
  import { shellDestination } from './lib/sessions/shellRoute.ts'
  import Tabs from './lib/ui/Tabs.svelte'
  import { rowOf, visibleTabs } from './lib/rooms/workViews.ts'
  import SessionMedia from './lib/sessions/SessionMedia.svelte'
  import ComputerPane from './lib/computers/ComputerPane.svelte'
  import Computers from './lib/computers/Computers.svelte'
  import ComputerPopout from './lib/computers/ComputerPopout.svelte'
  import ComputerPreview from './lib/computers/ComputerPreview.svelte'
  import { previewVisible, inUse } from './lib/computers/preview.ts'
  import { applyEvent, pushAction, openedApps, popOut } from './lib/computers/computers.ts'
  import TermDock from './lib/sessions/TermDock.svelte'
  import { legacyView } from './lib/sessions/termDock.ts'
  import { base } from './lib/files/repo.ts'
  import { available as hasTerminal } from './lib/sessions/TermPane.svelte'
  import Jobs from './lib/workflows/Jobs.svelte'
  import Prompt from './lib/ui/Prompt.svelte'
  import RunWorkflow from './lib/workflows/RunWorkflow.svelte'
  import GitHub from './lib/prs/GitHub.svelte'
  import type { Reference } from './lib/rooms/references.ts'
  import RoomSettings from './lib/rooms/RoomSettings.svelte'
  import { withoutReviewers } from './lib/prs/pr.ts'
  import { boot, watch, registerWorker } from './lib/settings/link.ts'
  import { engineWatch, browserEngine, type EngineStatus } from './lib/engineStatus.ts'
  import { loadLayout, saveLayout, lastRoom, saveLastRoom, loadDock, saveDock } from './lib/ui/prefs.ts'
  import { dmView } from './lib/sessions/dm.ts'
  import { followInternalLink, internalLinkHash, roomLabel, short } from './lib/ui/format.ts'
  import { parseDetailHref } from './lib/ui/details.ts'
  import { parsePRHash, parseIssueHash, parseFileHash } from './lib/prs/prlink.ts'
  import { ROOT_SESSION, parseRoute, routeHash, routeSession } from './lib/ui/route.ts'
  import { bindRouter, router } from './lib/app/router.svelte.ts'
  import { bundleReloadSafe, createBundleReload } from './lib/app/bundleReload.ts'
  import type { Drill } from './lib/app/router.svelte.ts'
  import MachinePage from './lib/machines/MachinePage.svelte'
  import { machinesSub } from './lib/machines/machines.ts'
  import { roomIDPattern as ROOM, postIDPattern as POST } from './lib/ui/posts.ts'
  import type { Room, Member, Post, Opened, Question, Visual, ProfileChoice, Attachment, CloseOutcome, RoomLimitField } from './lib/api/room.ts'
  import type { AttachmentRef } from './lib/api/engine.ts'
  import type { ApprovalDecision, ApprovalOpen } from './lib/api/approvals.ts'
  import type { Session as SessionData, QueueItem, MemberProfile } from './lib/api/session.ts'
  import type { Goal } from './lib/api/goal.ts'
  import type { RunList, RunDetail, RunSummary } from './lib/api/workflow.ts'
  import type { ShellJob } from './lib/api/terminal.ts'
  import type { ComputerInfo, ComputerEvent } from './lib/api/computer.ts'
  import type { StreamEvent, ListChange } from './lib/api/event.ts'
  import type { UpdateNotice } from './lib/api/settings.ts'
  import type { RowChange } from './lib/ui/listChanges.ts'
  import type { FeedMember } from './lib/rooms/feedViews.ts'
  import type { Receipt } from './lib/sessions/dm.ts'
  import type { Row as ChangeRow } from './lib/prs/prlist.ts'
  import type { CallRequest } from './lib/voice/callRequests.ts'
  import type { BootResult, RouteNotice } from './lib/settings/link.ts'
  import type { RunAgent } from './lib/api/workflow.ts'
  import { resolveAgent } from './lib/app/agentRef.ts'
  import type { AgentRef } from './lib/app/agentRef.ts'

  // A member the open view drills into. The root conversation is one made from the seat.

  interface DetailTarget {
    kind: string
    id: string
    roomId?: string
    turn?: number
  }
  let rooms = $state<Room[]>([])
  let roomsLoaded = $state(false)
  // started turns on once the first room is chosen. Before that, the
  // empty room is not the owner's choice and must not overwrite the saved one.
  let started = $state(false)
  // The open screen's state lives in lib/app/router.svelte.ts. The page reads
  // it through these aliases and writes it through router, so the template and
  // the code below name the screen as they always have.
  const roomId = $derived(router.roomId) // the open room, or '' for the all-rooms page
  const view = $derived(router.view) // the open view of the room
  const drill = $derived(router.drill) // the member whose transcript is open
  const sessionTurn = $derived(router.sessionTurn) // a completion link opens its exact stored turn
  const openJob = $derived(router.openJob) // the shell job selected on the session Shell tab
  const openRun = $derived(router.openRun) // the run whose detail is open, in the runs view
  const reviewOpen = $derived(router.reviewOpen)
  const reviewTab = $derived(router.reviewTab)
  const reviewVersion = $derived(router.reviewVersion)
  const readOnlyDm = $derived(router.readOnlyDm) // the open transcript came from Services: no header tabs, no composer
  const prLink = $derived(router.prLink) // a PR link from the address bar, for the Pull requests view
  const detailRequest = $derived(router.detailRequest) // the newest detail request
  // The router's navigation functions, called as they were when they lived here.
  const { openView, openReview, closeReview, routeHere, showRun, openPR, syncRoute, hashChanged, clearItemLink } = router
  let voiceHost = $state<ReturnType<typeof VoiceHost>>()
  let roomCalls = $state<CallRequest[]>([])
  let pageVisible = $state(document.visibilityState === 'visible')
  // machinesNav: the rail's machines tile is open, so the sidebar lists the
  // machines of this engine. Picking one opens its page in the main area. A
  // room in the link opens that room instead, so the list starts closed then.
  let machinesNav = $state(!new URLSearchParams(location.hash.slice(1)).has('room'))
  function machinesVisible() { return pageVisible && machinesNav && (narrow ? navOpen : showSide) }
  // The machine list and one machine's page live in
  // lib/app/machines.svelte.ts. The page reads them through these aliases, so
  // the template and the code below name them as they always have.
  const machines = $derived(machineView.machines)
  const machineId = $derived(machineView.machineId)
  const machineDetail = $derived(machineView.machineDetail)
  const machineError = $derived(machineView.machineError)
  const machineBusy = $derived(machineView.machineBusy)
  const machineProbing = $derived(machineView.machineProbing)
  const { openMachine, closeMachine, saveMachine, probeMachine } = machineView
  let home = $state<ReturnType<typeof Home>>() // the home screen, which takes live events
  // The open room's data lives in lib/app/roomLoader.svelte.ts. The page reads
  // it through these aliases and writes it through the loader, so the template
  // and the code below name the room data as they always have.
  const feed = $derived(roomData.feed)
  let room = $derived(feed?.room ?? null)
  // The reader is at the bottom of the feed; the Feed component binds this.
  let feedAtBottom = $state(true)
  const members = $derived(roomData.members)
  const hierarchy = $derived(roomData.hierarchy)
  const choices = $derived(roomData.choices)
  const unreadFrom = $derived(roomData.unreadFrom)
  const loadEarlier = roomData.loadEarlier // one bounded page per request
  const roomRequest = $derived(roomData.roomRequest) // room data can still load after a later view choice
  // The open session's transcript lives in lib/app/sessionView.svelte.ts. The
  // page reads it through these aliases, so the template keeps its names.
  const session = $derived(transcripts.session)
  const queue = $derived(transcripts.queue)
  const sessionError = $derived(transcripts.error)
  const threadCount = $derived(transcripts.threadCount)
  const { loadSession, loadEarlierSession, loadNewerSession, presentSession, showSession, unqueue, dismissPendingPost } = transcripts
  const layout = loadLayout({ side: true, members: true, threads: true })
  let threadsOpen = $state(layout.threads)
  let selectedThread = $state('')
  let jobs = $state<ShellJob[] | null>(null) // the room's shell jobs, newest first: one list for every view
  let jobsError = $state('')
  // The shell is exactly as tall as the screen shows, so a phone's browser bar or
  // keyboard can never leave the end of a page out of reach.
  $effect(() => trackViewportHeight())
  $effect(() => saveLayout({ threads: threadsOpen }))
  let showSide = $state(layout.side) // the room sidebar next to the rail
  // A narrow screen (a phone) has one column: the rail and the sidebar
  // become a drawer, and the members or pins panel a sheet over the feed.
  // The desktop window never gets this narrow (its minimum is 960 px).
  const NARROW = '(max-width: 760px)'
  let narrow = $state(window.matchMedia(NARROW).matches)
  let dockRequest = $state(0) // counts up to open the terminal dock
  let dockReview = $state({ request: 0, id: '', run: false }) // a terminal request to review once
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
  // The room's questions, visuals, pins, issues, sync counts and pull requests
  // live in lib/app modules. The page reads them through these aliases, so the
  // template and the code below name them as they always have.
  const questions = $derived(questionView.questions)
  const visuals = $derived(questionView.visuals)
  const pins = $derived(pinView.pins)
  const pinnedIds = $derived(pinView.ids)
  const syncRepo = $derived(syncView.repo)
  const syncRemotes = $derived(syncView.remotes)
  const syncNote = $derived(syncView.note)
  const issues = $derived(issueView.issues)
  const issueHistory = $derived(issueView.issueHistory)
  const { loadQuestions, loadVisuals, answerQuestion, openQuestion } = questionView
  const { loadPins, togglePin } = pinView
  const { loadIssues, openIssues, triageIssue, askCloseIssue } = issueView
  const { loadSync, syncAction } = syncView

  // The room's pull requests live in lib/app/prs.svelte.ts.
  const reviewer = $derived(changeView.reviewer)
  const changes = $derived(changeView.changes)
  const mergeVersion = $derived(changeView.mergeVersion)
  // issuesVersion counts committed issue changes, including new issue work, so the Issues tab reloads its tracked list.
  let issuesVersion = $state(0)
  const prError = $derived(changeView.error)
  const prMap = $derived(changeView.bySession)
  const openPRMap = $derived(changeView.openBySession)
  const { loadReviewer, reviewerSoon, loadChanges, untrackPR, trackPR, resumePR } = changeView

  // The page's side of a screen change, which the router asks for.
  bindRouter({
    started: () => started,
    pop: () => !!pop,
    followHash: () => hashTarget(),
    leaveRoom: () => { selectRoom('') },
    closePanels: () => { threadsOpen = false; showMembers = false; showPins = false; navOpen = false },
    closeDrawer: () => (navOpen = false),
    resolveView: (v) => feedViews.resolveView(v),
    trimFeedAtView: (next, previous) => roomData.trimAtView(next, previous),
    leaveScreen: (v, previous) => { reveal = ''; revealFolder = null; if (previous !== v) reply = null; if (v !== 'goals') goalId = '' },
    clearRunDetail: () => (runDetail = null),
    loadChanges,
    loadReviewer,
    loadRuns,
    loadRun,
  })
  // The address bar follows the open screen; see syncRoute.
  $effect(() => syncRoute())
  let needPair = $state(new URLSearchParams(location.hash.slice(1)).has('pair')) // a pairing link or a server 401 opens the pairing screen
  let halt = $state<BootResult | null>(null) // {key} or {error}: Connect stopped the app before it started
  let route = $state<RouteNotice | null>(null) // the route this page uses, for the sidebar
  let showClosed = $state(false)
  let showPins = $state(false) // the Pinned panel takes the member list's place
  let feedView = $state<ReturnType<typeof Feed>>()
  let reply = $state<Post | null>(null)
  let drafts = $state<Record<string, string>>({})
  let composerWork = $state<Record<string, boolean>>({})
  let composerPending = $derived(Object.values(composerWork).some(Boolean))
  function noteComposerWork(key: string, pending: boolean) {
    composerWork[key] = pending
  }
  const bundleReload = createBundleReload(() => location.reload())
  let bundleReloadPending = $state(false)
  const bundleSafety = () => bundleReloadSafe({ drafts, composerPending, voicePhase: $voiceCall.phase, visible: pageVisible })
  function offerBundleReload(version: string) {
    bundleReloadPending = true
    if (bundleReload.offer(version, bundleSafety())) bundleReloadPending = false
  }
  $effect(() => {
    const safe = bundleReloadSafe({ drafts, composerPending, voicePhase: $voiceCall.phase, visible: pageVisible })
    if (bundleReloadPending && bundleReload.retry(safe)) bundleReloadPending = false
  })
  let dataDir = $state('')
  let loadError = $state('')
  let health = $state('')
  let engineStatus = $state<EngineStatus>('') // why the event stream is down, see engineWatch
  let composer = $state<ReturnType<typeof Composer>>()
  let card = $state<Drill | null>(null) // the member whose profile modal is open
  let profile = $state<MemberProfile | null>(null)
  let profileError = $state('')
  let sessionView = $state<ReturnType<typeof Session>>()
  let viewing = $state<(Attachment & { roomId?: string; postId?: string }) | null>(null) // the attachment open in the viewer; roomId names its room when it is not this one (a call's shared file)
  let dropping = $state(false) // files are being dragged over the window
  let profiles = $state<ProfileChoice[]>([]) // what "/" completes in the composer
  let fresh = $state('') // a session just started from the composer: its row flashes
  let roomMenu = $state<{ r: Room; x: number; y: number; anchor?: HTMLElement } | null>(null) // {r, x, y, anchor}: the open room menu
  let goalId = $state('')
  // The room's goals live in lib/app/goals.svelte.ts. The page reads them
  // through these aliases, so the template and the code below name them as
  // they always have.
  const goals = $derived(goalView.goals)
  const knownGoals = $derived(goalView.knownGoals)
  const { loadGoals, loadRules, openGoal, expediteGoal, expediteItems, stopExpediting } = goalView
  // The room's one expedited goal, kept for the badges. A timer wakes it at the
  // deadline, so the badges leave on time without a server event.
  let expireTick = $state(Date.now())
  $effect(() => {
    const here = goalView.knownGoals.find((x) => isExpedited(x))
    if (here) goalView.loadItems(roomId, here)
  })
  $effect(() => {
    const now = Math.max(expireTick, Date.now())
    const scope = expediteScope(goalView.withItems(roomId, goalView.knownGoals), hierarchy, now)
    expedite.set({ ...scope, agents: new Set(scope.agents) })
    const until = scope.goalId ? scope.until : Number.POSITIVE_INFINITY
    if (!Number.isFinite(until)) return
    const timer = setTimeout(() => (expireTick = Date.now()), Math.min(until - Date.now() + 50, 2 ** 31 - 1))
    return () => clearTimeout(timer)
  })
  let selectedGoal = $derived(goalView.goalById(goalId))
  let phonePanelOpen = $derived(narrow && !!roomId && !!room && (showPins || (showMembers && !drill) || (view === 'goals' && !!selectedGoal)))
  let phonePanelLabel = $derived(view === 'goals' && selectedGoal ? `Goal ${selectedGoal.id}` : showPins ? 'Pinned posts' : 'Members')
  function closePhonePanel() {
    showPins = false
    showMembers = false
    goalId = ''
  }
  // A member selection in the phone sheet takes Escape first: it cancels the
  // selection and keeps the sheet open. The next Escape closes the sheet.
  function escapePhonePanel(): boolean {
    if (!selectingSessions) return false
    toggleSessionSelection()
    return true
  }
  // rules is the list in the Room settings Rules section.
  const rules = $derived(goalView.rules)
  // An inline rules edit keeps the list it started from until the edit closes.
  $effect(() => { goalView.applyPendingRules() })
  let confirm = $derived(confirmView.confirm)
  const confirmText = $derived(confirmView.confirmText)
  const { askConfirm, dismiss, runConfirm } = confirmView
  // The goal the room expedites now, which the replace text names.
  const expeditedGoalId = $derived($expedite.goalId)
  let consolidationAlive = true
  const controls = createRoomControls({
    api, room: () => roomId, request: () => roomRequest,
    alive: () => consolidationAlive, visible: () => visibleList('roomControls'),
    hierarchy: () => hierarchy, refresh: () => { loadMembers(); loadChanges() },
    error: (subject, text) => say({ tone: 'error', subject, text }), details: sayDetails,
  })
  const roomControls = $derived(controls.value)
  const roomControlsMenu = $derived(controls.menu)
  const clearDormantConfirm = $derived(controls.confirm)
  const { visit: visitRoomControls, openMenu: roomSessionActions, closeMenu: closeRoomSessionMenu, items: roomSessionItems, askClear: askClearDormant, clear: clearRoomDormant } = controls
  $effect(() => { controls.report() })
  // The member selection, its bulk panels and the consolidation screen live in
  // lib/app/selection.svelte.ts. The page reads them through these aliases, so
  // the template and the code below name them as they always have.
  const selectingSessions = $derived(selectionView.selectingSessions)
  const pickedSessions = $derived(selectionView.pickedSessions)
  const selectionMenu = $derived(selectionView.selectionMenu)
  const currentBulk = $derived(selectionView.currentBulk)
  const consolidating = $derived(selectionView.consolidating)
  const { selectionActions, closeSelectionMenu, selectionItems, moveUnder, selectForConsolidation, toggleSessionSelection, pickSession, closeConsolidation, consolidationChanged, runBulk, configureBulk, loadBulk, selectBulkFailures, closeBulk } = selectionView
  const sessionActions = createSessionActions({
    api,
    room: () => roomId, request: () => roomRequest, sessionVisit: () => transcripts.visit,
    selectedId: () => drill?.id,
    patchMember: (member) => { router.drill = member as Drill; transcripts.patchMember(member) },
    loadSession, loadMembers, loadProfiles, openSession, openMember, mentionMember,
    selectForConsolidation, moveUnder, expediteItems, askConfirm, say,
  })
  const menu = $derived(sessionActions.menu)
  const transferring = $derived(sessionActions.transferring)
  const expediting = $derived(sessionActions.expediting)
  const reopening = $derived(sessionActions.reopening)
  const dismissing = $derived(sessionActions.dismissing)
  const { menuItems, closeMenu, reopenSession, dismissSession, takeBackSeat } = sessionActions
  // The toast line and its detail list live in lib/app/toasts.svelte.ts.
  const toast = $derived(toasts.line)
  const toastDetail = $derived(toasts.detail)
  const toastDetailOpen = $derived(toasts.detailOpen)
  let closeResult = $state<{ name: string; outcomes: CloseOutcome[] } | null>(null)
  let runs = $state<RunList | null>(null) // roomsvc.RunList for the room
  let runsError = $state('')
  let runDetail = $state<RunDetail | null>(null)
  let runError = $state('')
  const settingsPanel = createSettingsPanel({
    hasRoom: () => !!room,
    closeNav: () => { navOpen = false },
    roomRequired: () => say({ tone: 'info', subject: 'Open a room first', text: 'Hook runs belong to a room repository.' }),
  })
  const { openSettings, openHookLog, createProfile } = settingsPanel
  let update = $state<UpdateNotice | null>(null) // configsvc.UpdateNotice: a newer dyna release
  let updateHidden = $state(false)
  let sessionTab = $state('chat') // the open tab of a session view
  let prView = $derived(view === 'issues')
  let winWidth = $state(typeof window === 'undefined' ? 1400 : window.innerWidth)
  const popMatch = /^#pop=(room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}):([A-Za-z0-9][A-Za-z0-9_.-]{0,95})$/.exec(typeof location === 'undefined' ? '' : location.hash)
  const pop = popMatch ? { room: popMatch[1], session: popMatch[2] } : null
  // The open room's computers live in lib/app/computers.svelte.ts. The page
  // reads them through these aliases, so the template keeps its names.
  const computers = $derived(computerView.computers)
  const computerFeed = $derived(computerView.feed)
  const computerError = $derived(computerView.error)
  const drillComputer = $derived(computerView.drillComputer)
  const sessionComputer = $derived(computerView.sessionComputer)
  const showPreview = $derived(computerView.showPreview)
  const { computerOf, loadComputers, handleComputer } = computerView
  const drillShells = $derived(drill ? runningShells(jobs, drill) : [])
  const shellTabJobs = $derived(drill ? sessionShellJobs(jobs, drill, openJob) : [])
  let reveal = $state('') // a file the Files view opens, from Changes
  let revealFolder = $state<{ path: string } | null>(null) // a folder the Files view shows, from Library; an object so the root and repeats still open
  let starting = $state(false) // the Run workflow dialog is open
  const roomActions = createRoomActions({
    roomId: () => roomId, selectRoom, loadRooms, say,
    renameRoom: api.renameRoom, deleteRoom: api.deleteRoom,
  })
  // The sidebar row of the open work view, and its tabs when it has several.
  let workRow = $derived(rowOf(view))
  let workTabs = $derived(visibleTabs(workRow))
  // The room feed is on screen: a feed view, not a work view or a session.
  let feedShown = $derived(!!room && !drill && isFeedView(view) && !workRow)

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

  const palette = createPalette({
    room: () => current ?? room, roomId: () => roomId, rooms: () => rooms,
    runs: () => runs, members: () => hierarchy, changes: () => changes,
    session: () => drill, sessionPRs: () => prMap[drill?.id ?? ''],
    narrow: () => narrow, hasTerminal: () => hasTerminal,
    openView: (v) => (v === 'terminal' ? dockRequest++ : openView(v)),
    openSettings: (section, pairNow) => openSettings(section, pairNow),
    renameRoom: (r) => roomActions.rename(r), deleteRoom: (r) => roomActions.delete(r),
    roomAction: (run, note) => roomActions.action(run, note), say: (message) => sayLine(message),
    loadRooms: () => loadRooms(), selectRoom: (id) => selectRoom(id),
    openFiles: (path) => { openView('files'); if (path) reveal = path.replace(/^\.?\//, '') },
    openPins: () => { showPins = true; if (narrow) showMembers = false },
    openRoomSettings: async (target) => { if (target.id === roomId || await selectRoom(target.id)) settingsPanel.openRoomSettings() },
    openSessionPRs: () => openReview('prs'),
    cancelRun: (run) => askConfirm({ kind: 'cancelrun', run }),
    toggleSidebar: () => (narrow ? (navOpen = !navOpen) : (showSide = !showSide)),
    startWorkflowDialog: () => { starting = true },
    openSession: (member) => openSession(member),
    openRun: (run) => showRun(run),
    openPR: (pr) => openPR(pr),
  })
  const showPalette = $derived(palette.show)
  const palettePath = $derived(palette.path)
  const searchText = $derived(palette.query)
  const paletteCommands = $derived(palette.paletteCommands)
  const composerCommands = $derived(palette.composerCommands)
  const targets = $derived(palette.targets)
  const { openSearch, closeSearch, openNewRoom, openTarget: openPaletteTarget, openRoom: paletteRoom } = palette

  // Where a file dropped on the window goes, as the drop line words it.
  const dropTarget = $derived(drill ? '@' + drill.name : roomLabel(current ?? room))
  let feedViews = $derived(feedModel(hierarchy))
  let viewLead = $derived(view.startsWith('channel:') ? feedViews.byId.get(view.slice('channel:'.length)) : null)
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
    return { id: 'root:' + roomId, rootSeat: true, role: 'root', name: 'root', address: room?.name + ':root', hue: owner?.hue ?? 0, computer: owner?.computer, state: 'idle', status: 'seat', profile: holder + ' holds the seat', seat: rootSeat, resumable: false, depth: 0, children: 0, turns: 0, created: '', lastActivity: '' }
  }
  // The breadcrumbs above a session and a run: root, then the parent sessions.
  let sessionTrail = $derived(drill && !readOnlyDm && !isRootDm ? parentTrail(feedViews, drill.parent, rootMember()).filter((m) => m.id !== drill?.id) : [])
  let runTrail = $derived(view === 'runs' && openRun && runDetail?.run?.parentId ? parentTrail(feedViews, runDetail.run.parentId, rootMember()) : [])
  let viewLabel = $derived(viewLead?.name ?? roomLabel(current ?? room))
  // Shell exit records stay out of feed views. A log link opens the session Shell tab.
  let posts = $derived((feed?.posts ?? []).filter((p) => !isShellResult(p) && (!isFeedView(view) || inView(p, view))))
  // What the owner chose to show in this feed view; hidden posts still count as read.
  let shownPosts = $derived(isFeedView(view) ? posts.filter(visibleTo(look, revealed) as (post: Post) => boolean) : posts)
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
      view, drill, reviewOpen, showRoomSettings: settingsPanel.showRoomSettings, memberChips, sidebarChips,
      sideVisible: narrow ? navOpen : showSide,
      controlsOpen: !!roomControlsMenu || !!clearDormantConfirm,
    })
  }
  const reload = createReloadGate(() => ({ room: roomId, visit: roomRequest }), visibleList)
  const readMarker = createReadMarker(api, {
    current: () => ({ room: roomId, visit: roomRequest + ':' + view, visible: pageVisible && !(narrow && reviewOpen) && consolidationAlive && !drill && isFeedView(view), chat: view === GENERAL }),
    read: () => { soonRooms(); soonRooms('feed') },
  })

  // The page's side of the room data, which the loader asks for. The loader
  // holds the feed and the member tree; the page owns the other lists.
  bindRoomLoader({
    visible: visibleList,
    resolveView: (v) => feedViews.resolveView(v),
    closedShown: () => showClosed,
    atBottom: () => feedAtBottom,
    leaveBottom: () => (feedAtBottom = false),
    error: (message) => (loadError = message),
    rooms: (value) => { rooms = value; roomsLoaded = true; loadError = '' },
    opened: applyRoomOpened,
    picked: (rows) => selectionView.applyRows(rows),
    loadComputers,
    refreshCounts: () => soonRooms(),
  })
  // The page's side of the goals, which lib/app/goals.svelte.ts asks for.
  bindGoals({
    reload,
    error: (message) => (loadError = message),
    openView: (name) => openView(name),
    selectGoal: (id) => (goalId = id),
    hidePins: () => (showPins = false),
    goalId: () => goalId,
    askReplace: (goal) => askConfirm({ kind: 'expedite', goal, roomId }),
    pickGoal: (member, held) => sessionActions.setExpediting({ member, goals: held }),
    hierarchy: () => hierarchy,
    applyPage: (value, append = false) => applyListPage('goals', value, append),
    say,
  })
  // The page's side of the machine views, which lib/app/machines.svelte.ts asks for.
  bindMachines({
    listShown: () => machinesVisible(),
    screenShown: () => pageVisible,
    narrow: () => narrow,
    setDrawer: (open) => (navOpen = open),
    say,
  })
  watchMachines()
  // The page's side of the questions, pins, issues, sync counts and pull
  // requests, which their modules ask for.
  bindQuestions({
    reload,
    error: (message) => (loadError = message),
    openView: (name) => openView(name),
  })
  // The Requests page opens the request's review in the dock; nothing runs yet.
  function reviewTerminalRequest(id: string) {
    dockRequest++
    dockReview = { request: dockRequest, id, run: false }
  }
  // The chat card's Run shows the exact command, so that click starts it.
  function runTerminalRequest(id: string) {
    dockRequest++
    dockReview = { request: dockRequest, id, run: true }
  }
  let approvals = $state<ApprovalOpen[]>([])
  async function loadApprovals() {
    const id = roomId
    const open = id ? await api.approvalList(id) : null
    if (id === roomId) approvals = open ?? []
  }
  // The open requests follow the room and every approval post the feed gets.
  let approvalPosts = $derived(posts.filter((p) => p.kind === 'approval').length)
  $effect(() => {
    void roomId
    void approvalPosts
    untrack(() => { loadApprovals() })
  })
  async function decideApproval(id: string, action: ApprovalDecision, reason: string) {
    await api.approvalDecide(roomId, id, action, reason)
    await loadApprovals()
  }
  async function declineTerminalRequest(id: string, reason: string) {
    await api.termRequestReport(roomId, id, 'declined', reason, false)
    await loadQuestions()
  }
  bindPins({
    reload,
    error: (message) => (loadError = message),
    clearError: () => (loadError = ''),
  })
  bindIssues({
    reload,
    error: (message) => (loadError = message),
    openIssuesView: () => { showPins = false; showMembers = false; openView('issues') },
    ask: roomActions.open,
    clearAsk: roomActions.dismiss,
    say,
  })
  bindSync({ reload })
  bindSessionView({
    pageOpen: () => pageVisible && !(narrow && reviewOpen),
    tab: () => sessionTab,
    say,
  })
  bindPRs({
    reload,
    applyPage: (value) => applyListPage('changes', value),
    alive: () => consolidationAlive,
    say,
  })
  // The page's side of the computers, which lib/app/computers.svelte.ts asks for.
  bindComputers({
    reload,
    tab: () => sessionTab,
    width: () => winWidth,
  })
  // The page's side of the confirm dialog, which lib/app/confirm.svelte.ts asks
  // for.
  bindConfirm({
    members: () => members,
    expedited: () => expeditedGoalId,
    closed: (name, outcomes) => (closeResult = { name, outcomes }),
    loadGoals,
    loadRuns,
    loadRun,
    loadMembers,
    loadChanges,
    say,
  })
  // The page's side of the member selection, which lib/app/selection.svelte.ts
  // asks for.
  bindSelection({
    visit: () => roomRequest,
    alive: () => consolidationAlive,
    hierarchy: () => hierarchy,
    people: () => people,
    newDetail: () => { router.detailRequest++ },
    // message writes the picked addresses into the room's draft and returns the
    // detail request it started, so a later focus can tell it is still current.
    message: (selected) => {
      clearItemLink()
      const focusRequest = ++router.detailRequest
      router.drill = null; card = null; reply = null; router.view = GENERAL
      const key = roomId + ':' + GENERAL
      const text = (drafts[key] ?? '').replace(/^\s*(?:@\S+(?:\s+|$))*/, '')
      drafts = { ...drafts, [key]: selected.map((m) => '@' + m.address).join(' ') + ' ' + text }
      if (narrow) showMembers = false
      return focusRequest
    },
    focusMessage: (request, alive) => { tick().then(() => { if (alive() && request === router.detailRequest) composer?.focus() }) },
    closeSessionMenu: sessionActions.dismissMenu,
    showMembers: () => { showMembers = true; showPins = false },
    loadMembers,
    loadChanges,
    loadSession: () => { if (drill) loadSession() },
  })
  function loadRooms() { return roomData.loadRooms() }
  function loadMembers() { return roomData.loadMembers() }
  // applyRoomOpened shows the opened room's other lists, which the page owns.
  function applyRoomOpened(value: Opened) {
    pinView.setPins(value.pins ?? [])
    loadSync()
    loadIssues()
    loadIssues(true)
    runs = value.runs ?? null
    runsError = ''
    workflowData.loadJobs()
    loadQuestions()
  }
  // Profiles change from the CLI or the Settings view; the composer asks
  // again each time "/" opens its list.
  function loadProfiles() {
    api.profiles().then((p: ProfileChoice[] | null) => (profiles = p ?? [])).catch(() => {})
  }

  $effect(() => { if (settingsPanel.showRoomSettings) untrack(loadProfiles) })

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
  bindLists({
    current: () => ({ room: roomId, visit: roomRequest }),
    patch: (kind, load) => reload.patch(kind, load),
    goals: (id, cursor) => api.goals(id, cursor),
    changes: (id, cursor) => api.changes(id, cursor),
    jobs: (cursor) => workflowData.loadJobs(cursor),
    setGoals: (rows) => goalView.setGoals(rows),
    setChanges: (rows) => changeView.setChanges(rows),
    error: (message) => (loadError = message),
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

  // Pins and links open the feed view that contains the requested post.
  async function jumpTo(id: string) {
    const request = ++router.detailRequest
    const rid = roomId
    router.drill = null
    card = null
    viewing = null
    // Leave the bottom first, so a live post does not trim what this loads.
    roomData.leaveBottom()
    if (!(feed?.posts ?? []).some(post => post.id === id)) await loadEarlier('around', id)
    if (request !== detailRequest || rid !== roomId) return
    clearItemLink()
    const post = (feed?.posts ?? []).find((p) => p.id === id)
    if (post && questionID(post)) { openQuestion(post); return }
    if (post && isShellResult(post)) { await openShellLog(shellJobID(post), shellParent(post)); return }
    // A voice call line has no feed view; Voice's call widget shows it.
    if (post && inView(post, VOICE)) return
    router.view = post ? homeView(post) : GENERAL
    await tick()
    if (request === detailRequest && rid === roomId) feedView?.jump(id)
  }

  // A visible transcript tab keeps the open session read; a hidden one waits.
  $effect(() => {
    const tab = sessionTab
    const visible = transcripts.sessionVisible(tab)
    untrack(() => { if (drill && visible) loadSession() })
  })
  function markShown(list: Post[]) { return readMarker.mark(list) }

  // Only messages visible in the selected feed view become read.
  $effect(() => {
    if (!drill && isFeedView(view)) markShown(posts)
  })

  // Read navigation here; list state reads inside loaders must not become
  // dependencies that schedule another refresh after every response.
  $effect(() => {
    roomId; view; drill; settingsPanel.showRoomSettings; memberChips; sidebarChips; pageVisible
    untrack(() => {
      loadChanges()
      loadReviewer()
      loadGoals()
      loadRules()
      loadVisuals()
    })
  })

  let canLoadControls = $derived(visibleList('roomControls'))
  let canLoadComputers = $derived(visibleList('computers'))
  let canLoadMentions = $derived(visibleList('mentions'))
  $effect(() => {
    roomId; canLoadControls
    untrack(() => { if (canLoadControls) controls.load() })
  })
  $effect(() => {
    roomId; canLoadComputers
    untrack(() => { if (canLoadComputers) loadComputers() })
  })
  $effect(() => {
    roomId; canLoadMentions
    untrack(() => { if (canLoadMentions) roomData.loadMentions() })
  })

  async function mentionReference(reference: Reference) {
    if (reference.kind === 'room_issue' && !reference.url) reference = { ...reference, url: `#room=${encodeURIComponent(roomId)}&view=issues` }
    openView('all')
    await tick()
    composer?.addReference(reference)
    composer?.focus()
  }

  // selectRoom loads a room and returns whether its navigation is still current.
  // One call brings the first screen; the room list refreshes beside it.
  async function selectRoom(id: string, startView = GENERAL) {
    clearItemLink(id, startView)
    const request = ++router.detailRequest
    roomData.newVisit()
    navOpen = false
    machineView.clear()
    machinesNav = !id
    if (id !== roomId) {
      settingsPanel.closeRoomSettings()
      selectionView.clear()
    }
    if (id !== roomId) computerView.clear()
    router.roomId = id
    // Goal IDs are per room; a goal fetched for a link belongs to the old one.
    goalView.clear()
    visitRoomControls(id)
    router.drill = null
    card = null
    transcripts.clear()
    router.sessionTurn = 0
    router.openJob = ''
    reply = null
    router.reviewOpen = startView === 'files' || startView === 'github' || startView === 'issues'
    router.reviewTab = startView === 'github' ? 'prs' : startView === 'issues' ? 'issues' : 'files'
    // TODO: legacy/shim remove later — the terminal view became the dock.
    const start = legacyView(startView, GENERAL)
    if (start.dock) { saveDock(id, { ...loadDock(id), open: true }); dockRequest++ }
    router.view = ['all', 'owner', 'turns', 'files', 'github'].includes(start.view) ? GENERAL : start.view
    roomData.clearMembers()
    runs = null
    listView.clear()
    jobs = null
    jobsError = ''
    router.openRun = ''
    runDetail = null
    pinView.clear()
    issueView.clear()
    questionView.clear()
    changeView.clear()
    goalId = ''
    if (!id) {
      roomData.clearFeed()
      return true
    }
    if (!await roomData.open(0, true)) return false
    return request === detailRequest
  }

  // openMember shows a member's profile card. The owner has no card.
  async function openMember(m: Member | { rootSeat: true } | null | undefined) {
    if (m && 'rootSeat' in m && m.rootSeat) { openSession(m); return }
    router.detailRequest++
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
    // A usage row names only the session, so the roster supplies its computer.
    openSession(resolveAgent(m, hierarchy))
  }

  // chooseCloseRow opens the work a failed close step left behind, where the
  // owner can resolve it: hand a goal to an agent, move a child, or read a PR.
  function chooseCloseRow(choice: CloseChoice, row: CloseRow) {
    closeResult = null
    const target = row.target
    if (!target) return
    if (choice === 'move-goal' && target.kind === 'goal') openGoal(target.id)
    else if (choice === 'open-pr' && target.kind === 'pr') openPR(target.number)
    else if (choice === 'move-child' && target.kind === 'session') {
      const name = target.name
      const member = hierarchy.find((m) => m.name === name)
      if (member) openSession(member)
    }
  }

  // openSession drills into a member's full session view.
  function openSession(source: Member | FeedMember | AgentRef | { rootSeat: true } | null, turn = 0, initial: SessionData | null = null, readOnly = false) {
    if (!source) return
    const m = ('rootSeat' in source && source.rootSeat ? rootMember() : source) as Drill
    clearItemLink()
    router.detailRequest++
    transcripts.nextVisit()
    card = null
    showPins = false
    if (narrow) showMembers = false
    if (drill?.id !== m.id || sessionTurn !== turn) {
      transcripts.openFresh()
      sessionTab = 'chat'
      selectedThread = ''
    }
    router.drill = m
    router.readOnlyDm = readOnly
    router.sessionTurn = turn
    router.openJob = ''
    if (initial) transcripts.setSession(initial)
    else loadSession()
    setTimeout(() => sessionView?.focus(), 0)
  }

  // The root seat has no session of its own to send to; @root reaches whoever holds it.
  // The post is written in the seat's DM, so the seat reads it there.
  async function sendToSession(text: string, atts: AttachmentRef[] = [], forwardOf = '') {
    if (!drill) return
    const res = drill.rootSeat ? await api.send(roomId, '@root ' + text, '', atts, forwardOf, [], seatDMConversation(roomId)) : await api.sendToSession(drill.id, text, atts, forwardOf)
    openSession(drill)
    return res
  }

  function openAddress(addr: string) {
    if (!room || addr === room.name) {
      router.drill = null
      return
    }
    const m = members.find((x) => x.address === addr)
    if (m) openMember(m)
  }

  function mentionMember(m: Member) {
    card = null
    router.drill = null
    const cur = draft.value.trim()
    draft.value = `@${m.name} ${cur.startsWith('@') ? cur.replace(/^(@\S+\s*)+/, '') : cur}`.trimEnd() + ' '
    setTimeout(() => composer?.focus(), 0)
  }

  // startReply targets the post's sender, which the server names from the
  // stored identity of the post (Post.reply). The target carries the address
  // that session answers at now, across rooms and after a rename, so this only
  // writes it. A target with no address names nobody, such as a session whose
  // name another session took, and a post with no target, such as one the
  // owner typed, leaves the draft's recipients alone. The reply replaces the
  // draft's leading recipients (a kept '@viewer ' included) and keeps any text
  // already written after them.
  function startReply(p: Post) {
    reply = p
    if (!room) return
    const target = p.reply
    if (target?.address) {
      const body = draft.value.replace(/^\s*(?:@\S+(?:\s+|$))*/, '')
      draft.value = `@${short(target.address, room.name)} ${body}`
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

  async function linkPRGoal(change: ChangeRow, goalId: string) {
    const id = roomId
    // A GitHub-only row has no record to hold its goal yet. Track it for the owner first.
    if (change.number === undefined) return
    const row = change.key ? change : await api.trackPR(id, '', change.number)
    if (!row.key) return
    await api.setPRGoal(id, row.key, goalId)
    if (id === roomId) await loadChanges()
  }
  // Room menu: everyday actions first, then Delete in coral, which asks for
  // the room's name.
  function roomMenuItems(r: Room) {
    return palette.roomMenu(r)
  }

  // openRunAgent opens the session that ran an agent: its transcript and its composer.
  function openRunAgent(run: RunSummary, a: RunAgent) {
    if (!a.session) return
    openAgent({ id: a.session, name: a.label || `agent ${a.id}`, hue: 0, roomId: run.roomId })
  }

  async function runStarted(id?: string) {
    starting = false
    say({ tone: 'success', subject: 'Workflow started', text: 'Its agents run now. Open Runs to follow it.', action: id ? { label: 'Open', run: () => showRun({ id }) } : undefined })
    await loadRuns()
    if (id) showRun({ id })
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
      if (pending.has('controls')) controls.load()
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
    if (drill && transcripts.sessionVisible()) loadSession()
  }

  // resync reloads what the app shows after the web event stream was lost:
  // events may have been missed while it was down.
  async function resync() {
    changeView.bumpVersion()
    loadRooms()
    if (!roomId) return
    // Keep loaded history on screen when the event stream reconnects.
    if (!await roomData.open(Math.max(feed?.posts?.length ?? 0, 50) + 20)) return
    controls.load()
    if (drill) { loadSession(); router.reviewVersion++ } // events may have been missed while the stream was down
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
    voiceHost?.openLink(callTarget(location.hash))
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
    router.routing++
    // A shell link names no screen of its own, so it always reaches openDetail,
    // which opens the owning session's Shell tab.
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
        router.prLink = issue || pr
      }
    }).finally(() => {
      router.routing--
      router.routeFix = true
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
        if (current()) changeView.setChanges(applyListItem('changes', change, changes))
      })
      return true
    }
    if (ev.kind === 'jobs') {
      workflowData.patchJobs(ev.change)
      return true
    }
    if (ev.kind === 'questions') {
      reload.patch('questions', (current) => {
        if (current()) questionView.setQuestions(patchListRows(questions, change as RowChange<Question>))
      })
      return true
    }
    if (ev.kind === 'goals') {
      reload.patch('goals', (current) => {
        if (!current()) return
        goalView.setGoals(applyListItem('goals', change, goals))
        goalView.trackFetchedGoal(change)
      })
      return true
    }
    return false
  }

  function handle(ev: StreamEvent) {
    if (ev.kind === 'build') {
      offerBundleReload(ev.version)
      return
    }
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
      if (roomControls?.loading || roomControls?.state?.paused !== false) controls.load()
    }
    if (ev.kind === 'posts' && feed && evRoom === feed.room.id) roomData.applyPosts(ev)
    if (ev.kind === 'goals' && evRoom === roomId) { if (!item) loadGoals(); loadRules(); loadIssues(); loadIssues(true) }
    if (ev.kind === 'machines') machineView.changed()
    if (ev.kind === 'pins' && evRoom === roomId) loadPins()
    if (ev.kind === 'issues' && evRoom === roomId) { loadIssues(); loadIssues(true); issuesVersion++ }
    if (['room.paused', 'room.resumed'].includes(ev.kind) && (!evRoom || evRoom === roomId)) controls.load()
    // A prs event without a row says the room's PR settings changed.
    if ((ev.kind === 'prs' || ev.kind === 'changes') && (!evRoom || evRoom === roomId)) {
      changeView.bumpVersion()
      reviewerSoon()
      loadIssues()
      loadIssues(true)
      if (ev.kind === 'changes' && !item) loadChanges()
    }
    if (ev.kind === 'session' && evRoom === roomId && ev.change?.id === drill?.id) { loadSession(); router.reviewVersion++ }
    if (ev.kind === 'members' && evRoom === roomId) {
      if (!item) loadMembers()
      if (ev.change?.controls) soonRooms('controls')
    }
    if (ev.kind === 'posts' && drill && evRoom === roomId) {
      transcripts.schedule()
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

  const shortcut = createShortcut({
    paletteOpen: () => showPalette, openSearch, closeSearch,
    toggleSidebar: () => { showSide = !showSide },
    selectingSessions: () => selectingSessions, toggleSessionSelection,
    drillOpen: () => !!drill, threadsOpen: () => threadsOpen, closeThreads: () => { threadsOpen = false },
    viewing: () => !!viewing, cardOpen: () => !!card, goalOpen: () => !!goalId,
    closeGoal: () => { goalId = '' }, pinsOpen: () => showPins, closePins: () => { showPins = false },
    narrow: () => narrow, closePhonePanels: () => { navOpen = false; showMembers = false },
  })

  // shellParent is the session a shell result names, empty when the owner started it.
  function shellParent(post: { payload?: unknown }): string {
    const payload = post.payload
    if (!payload || typeof payload !== 'object' || !('parent' in payload)) return ''
    const parent = payload.parent
    return typeof parent === 'string' ? parent : ''
  }

  // openShellLog opens the owning session's Shell tab with this job selected and its log open.
  // A shell with no session, such as a root or hook shell, opens on the root seat.
  async function openShellLog(id: string, owner = '') {
    const request = router.detailRequest
    let who = owner || (jobs ?? []).find((j) => j.id === id)?.owner || ''
    if (!who) {
      try {
        const page = await api.shellJobs(roomId, '', 0)
        if (request !== detailRequest) return
        who = (page.items ?? []).find((j) => j.id === id)?.owner || ''
      } catch {
        if (request !== detailRequest) return
      }
    }
    if (request !== detailRequest) return
    const dest = shellDestination(id, who, hierarchy)
    if (dest.session === ROOT_SESSION) openSession({ rootSeat: true })
    else {
      const member = hierarchy.find((m) => m.id === dest.session)
      if (member) openSession(member)
      else {
        try {
          const data = await api.transcript(dest.session)
          if (request !== detailRequest) return
          openSession(data.member, 0, data)
        } catch {
          if (request !== detailRequest) return
          openSession({ rootSeat: true })
          say({ tone: 'info', subject: 'Opened the log on root', text: 'That shell has no open session.' })
        }
      }
    }
    sessionTab = 'shell'
    router.openJob = id
    router.view = GENERAL
  }

  // Completion links open stored UI details. They never invoke a command.
  async function openDetail(target: DetailTarget) {
    let request = ++router.detailRequest
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
      await openShellLog(target.id)
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

  // Load workflow targets only while the palette is open.
  $effect(() => { if (showPalette && roomId && runs === null) untrack(loadRuns) })

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
    const off = (onEvent as (event: (ev: StreamEvent) => void, state: (up: boolean) => void) => () => void)((ev) => { handle(ev); if (ev.kind !== 'build') voiceHost?.event(ev) }, (up) => { link.streamState(up); engine.state(up) })
    registerWorker()
    const fromWorker = (e: MessageEvent) => {
      if (e.data?.type !== 'open') return
      voiceHost?.openLink(callLink(e.data.room, e.data.call ?? null))
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
      const internal = internalLinkHash(a.href, location)
      if (internal !== null) {
        followInternalLink(internal, location)
        return
      }
      if (/^https?:/.test(a.href)) openExternal(a.href)
    }
    document.addEventListener('click', clicks)
    const ticker = setInterval(() => (rooms = [...rooms]), 30000) // keep "ago" labels fresh
    transcripts.start()
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
      selectionView.refreshTransfers()
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
      controls.destroy()
      selectionView.destroy()
      consolidationAlive = false
      clearInterval(transferPoll)
      roomData.destroy()
      transcripts.destroy()
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
  <NavPane open={navOpen} {narrow} suspended={phonePanelOpen} {showSide} {rooms} selected={machinesNav ? '' : roomId} onselect={selectRoom} onclose={() => (navOpen = false)} update={!!update} onsettings={() => openSettings()} onlogout={signOut} onmenu={(r: Room, x: number, y: number, anchor?: HTMLElement) => (roomMenu = { r, x, y, anchor })} sidebar={{ machines, machinesOpen: machinesNav, machineId, onmachine: openMachine, selected: drill?.address ?? '', onroom: (id: string) => selectRoom(id), computers, viewUnread: feed?.viewUnread ?? {}, room: current ?? room, prs: openPRMap, onroommenu: (x: number, y: number, anchor?: HTMLElement) => { const r = current ?? room; if (r) roomMenu = { r, x, y, anchor } }, view: view as typeof GENERAL, requests: ownerRequestCount(questions), live, runs, dataDir, ownerHue, route, onview: openView, onmember: openAgent }} />
  {#if narrow && navOpen && !phonePanelOpen}
    <button class="scrim" aria-label="Close the room list" onclick={() => (navOpen = false)}></button>
  {/if}
  <main class="main">
    <PageHeader
      {narrow}
      {navOpen}
      backgroundInert={navOpen || phonePanelOpen}
      {showSide}
      {roomId}
      {room}
      {current}
      {drill}
      {session}
      {machines}
      {machineId}
      {machineDetail}
      {readOnlyDm}
      {reviewOpen}
      {people}
      {live}
      {pins}
      bind:sessionTab
      {sessionComputer}
      {drillComputer}
      shells={drillShells.length}
      {isRootDm}
      {threadsOpen}
      {threadCount}
      {sessionTrail}
      {view}
      {openRun}
      {runTrail}
      {runDetail}
      {workRow}
      {workTabs}
      {syncRemotes}
      {syncRepo}
      {syncNote}
      {showPins}
      {showMembers}
      onnavopen={() => (navOpen = true)}
      ontoggleside={() => (showSide = !showSide)}
      onback={() => (router.drill = null)}
      onsession={openSession}
      onmember={openMember}
      onthreads={() => { threadsOpen = !threadsOpen; selectedThread = ''; if (threadsOpen) sessionTab = 'chat' }}
      oncloserun={() => (router.openRun = '')}
      onview={openView}
      onsyncrefresh={() => syncAction(api.syncRefresh)}
      onsyncexport={() => syncAction(api.gitExport)}
      onsearch={() => openSearch()}
      ontogglepins={() => { showPins = !showPins; if (showPins) showMembers = false }}
      onreview={() => (reviewOpen ? closeReview() : openReview())}
      ontogglemembers={() => { showMembers = !showMembers; if (showMembers) showPins = false }}
    />
    <div class="content">
      <div class="conversation-workspace" inert={narrow && (navOpen || phonePanelOpen)}>
      {#key viewIdentity({ roomId, view, drill })}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="center" inert={narrow && reviewOpen} in:fade={{ duration: prView ? 0 : motion(FAST) }} ondragover={dragOver} ondragleave={(e) => { if (!e.currentTarget.contains(e.relatedTarget instanceof Node ? e.relatedTarget : null)) dropping = false }} ondrop={drop}>
        <PageNotices {dropping} {dropTarget} {engineStatus} {health} {update} {updateHidden} {roomId} {loadError} {bundleReloadPending} onupdatehidden={() => (updateHidden = true)} onbundlereload={() => { if (bundleReload.retry(true)) bundleReloadPending = false }} />
        {#if !roomId && machineId}
          <MachinePage machine={machineDetail} {rooms} error={machineError} busy={machineBusy} probing={machineProbing} onback={closeMachine} onagent={openAgent} onsave={saveMachine} onprobe={probeMachine} />
        {:else if !roomId}
          <Home bind:this={home} {rooms} {roomsLoaded} {update} onnewroom={() => openNewRoom()} onroom={selectRoom} onaccount={(id: string) => openSettings('accounts', false, '', id)} onsettings={(pane: string) => openSettings(pane)} />
        {:else if !room}
          <Skeleton label="Loading room" />
        {:else if drill}
          <!-- Keyed: staged files and the typed draft belong to one session. -->
          {#if narrow && !readOnlyDm}<SessionTabs bind:tab={sessionTab} media={session?.media ?? []} computer={sessionComputer} shells={drillShells.length} />{/if}
          {#key drill.id}
            {#if sessionTab === 'computer' && computers}
              <ComputerPane c={computerOf(drill.computer) ?? { session: drill.computer ?? '', name: drill.name, state: 'none', apps: [], stream: {}, viewers: 0, rssKiB: 0, streamRssKiB: 0 }} room={room.id} feed={computerFeed[drill.computer ?? ''] ?? []} chat={{ computerCount: 1, draft: drafts['computer:' + roomId + ':' + drill.id] ?? '', ondraft: computerDraft, onpending: (pending: boolean) => noteComposerWork('computer:' + drill.id, pending), session, room, members: hierarchy, onsend: sendToSession, onopenfile: (a: Attachment) => (viewing = a), onpost: jumpTo, onmessages: () => (sessionTab = 'chat') }} />
            {:else if sessionTab === 'shell'}
              <Jobs older={listView.pages.jobs?.older} loadingOlder={listView.olderLoading} onolder={() => loadOlderLists(['jobs'])} jobs={shellTabJobs} error={jobsError} jobId={openJob} {narrow} onstop={stopJob} />
            {:else if sessionTab === 'media'}
              <SessionMedia onpost={jumpTo} {room} posts={session?.media ?? []} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
            {:else}
              <div class="sess-wrap" style:--shift={threadsOpen && !narrow ? '360px' : '0px'}>
              <Session readOnly={readOnlyDm} onstop={(m: Member) => askConfirm({ kind: 'stop', m })} bind:readingHistory={transcripts.readingHistory} onissue={openIssues} onreopen={reopenSession} {reopening} ondismisssession={dismissSession} {dismissing} onpost={jumpTo} bind:this={sessionView} bind:threadsOpen bind:selectedThread bind:draft={sessionDraft.value} members={hierarchy} jobs={jobs ?? []} onchild={openSession} onjob={(job) => openShellLog(job.id, job.owner ?? '')} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} {session} error={sessionError} {room} {queue} focusedTurn={sessionTurn} onrecent={() => openSession(drill)} onearlier={loadEarlierSession} onnewer={loadNewerSession} onpresent={presentSession} onunqueue={unqueue} ondismiss={dismissPendingPost} onharness={(name) => openSettings('profiles', false, name)} onsend={sendToSession} onpending={(pending) => noteComposerWork('session:' + drill.id, pending)} />
              {#if showPreview}<ComputerPreview c={drillComputer} session={drill.computer ?? ''} name={drill.name} onopen={() => (sessionTab = 'computer')} />{/if}
              </div>
            {/if}
          {/key}
        {:else}
        {#if workTabs && narrow}<Tabs tabs={workTabs} tab={view} label={workRow?.label ?? ''} onselect={openView} />{/if}
        {#if view === 'library'}
          {#key room.id}<Library roomId={room.id} {narrow} onopen={({ path, dir }) => { openView('files'); if (dir) revealFolder = { path }; else reveal = path }} />{/key}
        {:else if view === 'requests'}
          {#key room.id}<Requests roomId={room.id} {questions} focusId={questionView.focus} focusRequest={questionView.focusRequest} onanswer={answerQuestion} onrun={reviewTerminalRequest} ondecline={declineTerminalRequest} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />{/key}
        {:else if view === 'services'}
          {#key room.id}<Services roomId={room.id} repo={room.repo ?? ''} members={hierarchy} changes={changes ?? []} onopen={(m: Member) => openSession(m, 0, null, true)} onpr={openPR} />{/key}
        {:else if view === 'visuals'}
          {#key room.id}<Visuals roomId={room.id} {visuals} onopen={(v: Visual) => (viewing = { ...v.attachment, postId: v.post.id })} />{/key}
        {:else if view === 'computers' && computers}
          {#key room.id}<Computers list={computers} error={computerError} onopen={(c: ComputerInfo) => { const m = hierarchy.find((x) => x.id === c.session); if (m) { openSession(m); sessionTab = 'computer' } else popOut(room.id, c.session) }} />{/key}
        {:else if view === 'goals'}
          {#key room.id}<Goals roomId={room.id} older={listView.pages.goals?.older} loadingOlder={listView.olderLoading} onolder={() => loadOlderLists(['goals'])} {goals} members={hierarchy} selected={goalId} onselect={(id: string) => (goalId = id)} ongoal={openGoal} onexpedite={(g: Goal) => expediteGoal(g).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) }))} />{/key}
        {:else if view === 'runs'}
          {#if openRun}
            {#key openRun}<Run detail={runDetail} error={runError} oncancel={(run) => askConfirm({ kind: 'cancelrun', run })} onpause={pauseRun} onopen={openRunAgent} />{/key}
          {:else}
            <Runs {room} list={runs} error={runsError} onopen={showRun} onstart={() => (starting = true)} />
          {/if}
        {:else}
          <Feed {goals} {profiles} onnewprofile={() => openSettings('profiles')} bind:this={feedView} bind:stuck={feedAtBottom} label={viewLabel} {room} posts={shownPosts} onreveal={revealPost} {questions} calls={view === GENERAL ? roomCalls : []} onacceptcall={(req) => voiceHost?.accept(req)} ondeclinecall={(req) => voiceHost?.decline(req)} {view} earlier={feed?.earlier ?? 0} onearlier={(near: () => boolean) => loadEarlier('older', '', near)} newer={feed?.newer ?? 0} onnewer={(near: () => boolean) => loadEarlier('newer', '', near)} onpresent={() => loadEarlier('present')} onaround={(id: string) => loadEarlier('around', id)} {unreadFrom} pinned={pinnedIds} onaddress={openAddress} onreply={startReply} onforward={(p: Post) => composer?.forward(p)} onpin={togglePin} ondetail={openDetail} onquestion={openQuestion} onreviewrequest={runTerminalRequest} ondeclinerequest={declineTerminalRequest} {approvals} ondecideapproval={decideApproval} ongoal={openGoal} onissue={openIssues} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
          <!-- Keyed: staged files belong to one room and never follow a switch. -->
          {#key room.id + view}
            <Composer onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} bind:this={composer} label={viewLabel} prepareText={(text: string) => prepareFeedText(text, viewLead ?? null, feedViews, (value) => api.preview(room.id, value))} {room} {choices} {profiles} commands={composerCommands} onslash={loadProfiles} {reply} bind:draft={draft.value} onsend={send} oncancelreply={() => (reply = null)} onpending={(pending) => noteComposerWork('feed:' + room.id + ':' + view, pending)} />
          {/key}
        {/if}
        {/if}
      </div>
      {/key}
      {#if room && reviewOpen}
        {#key room.id + ':' + (drill?.rootSeat ? '' : drill?.id ?? '')}
          <ReviewWorkspace roomId={room.id} session={drill?.rootSeat ? '' : drill?.id ?? ''} sessionName={drill?.name ?? ''} members={hierarchy} bind:tab={router.reviewTab} {reveal} folder={revealFolder} version={reviewVersion} onpr={openPR} onclose={closeReview} onselect={(id) => { const member = hierarchy.find((m) => m.id === id); if (member) openSession(member); else { router.drill = null; router.view = GENERAL } }}>
            {#snippet prs()}
              <GitHub {mergeVersion} older={listView.pages.changes?.older} loadingOlder={listView.olderLoading} failedOlder={listView.olderFailed} onolder={() => loadOlderLists(['changes'])} roomId={room.id} kind="pr" active={reviewTab === 'prs'} session={routeSession(drill)} link={prLink} {changes} members={hierarchy} onmention={mentionReference} onsession={(m) => openSession(m)} onpr={openPR} ontrack={trackPR} onuntrack={untrackPR} onresume={resumePR} {reviewer} onreviewer={loadReviewer} goals={knownGoals} onexpedite={expediteGoal} onlinkgoal={linkPRGoal} />
            {/snippet}
            {#snippet issuesTab()}
              <GitHub {mergeVersion} older={listView.pages.changes?.older} loadingOlder={listView.olderLoading} failedOlder={listView.olderFailed} onolder={() => loadOlderLists(['changes'])} roomId={room.id} kind="issue" {issuesVersion} active={reviewTab === 'issues'} localIssues={[...issues, ...issueHistory]} oncloseissue={askCloseIssue} onexportissue={(i) => triageIssue(() => api.exportIssue(roomId, i.id))} ondismississue={(i) => triageIssue(() => api.closeIssue(roomId, i.id, 'Dismissed'))} integration={syncRepo?.integration as import('./lib/api/lanesync.ts').Integration | null} link={prLink} {changes} members={hierarchy} onmention={mentionReference} onsession={(m, tab) => { openSession(m); if (tab === 'prs') openReview('prs'); else if (tab) sessionTab = tab }} onpr={openPR} ontrack={trackPR} onuntrack={untrackPR} onresume={resumePR} {reviewer} onreviewer={loadReviewer} goals={knownGoals} onexpedite={expediteGoal} onlinkgoal={linkPRGoal} />
            {/snippet}
          </ReviewWorkspace>
        {/key}
      {/if}
      </div>
      {#if phonePanelOpen}
        <button class="scrim" aria-label="Close the panel" onclick={closePhonePanel}></button>
      {/if}
      <!-- On a wide screen this wrapper adds no box; on a narrow one it is a dialog sheet. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="panel" use:dialog={{ active: phonePanelOpen, close: closePhonePanel, escape: escapePhonePanel }} role={phonePanelOpen ? 'dialog' : undefined} aria-modal={phonePanelOpen ? 'true' : undefined} aria-label={phonePanelOpen ? phonePanelLabel : undefined} tabindex={phonePanelOpen ? -1 : undefined}>
      {#if roomId && room && view === 'goals' && selectedGoal}
        {#key selectedGoal.id}<GoalDetail roomId={room.id} goal={selectedGoal} members={hierarchy} onchanged={loadGoals} onclose={() => (goalId = '')} onexpedite={expediteGoal} />{/key}
      {:else if roomId && room && showPins}
        <Pins {room} {pins} ondetail={openDetail} onjump={(id: string) => { if (narrow) showPins = false; jumpTo(id) }} onunpin={togglePin} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} onclose={() => (showPins = false)} />
      {:else if roomId && room && showMembers && !drill}
        <Members computers={computers ?? []} onopencomputer={(m: Member) => { openSession(m); sessionTab = 'computer' }} members={people} focus={view.startsWith('channel:') ? view.slice('channel:'.length) : ''} scope="{room.id}|{view}" {roomControls} onroomactions={roomSessionActions} onresumeroom={() => controls.run('resume')} onreloadcontrols={() => controls.load()} oncleardormant={askClearDormant} ondismiss={(m: Member) => dismissSession(m).catch((e) => say({ tone: 'error', subject: `Could not dismiss ${m.name}`, text: errorText(e) }))} {dismissing} ontakeseat={takeBackSeat} {fresh} selecting={selectingSessions} picked={pickedSessions} onselection={toggleSessionSelection} onpick={pickSession} onactions={selectionActions} selected={''} closed={showClosed} onselect={openMember} onmenu={(m: Member, x: number, y: number, anchor?: HTMLElement) => sessionActions.openMenu(m, x, y, anchor)} onclosed={() => { showClosed = !showClosed; loadMembers() }} />
      {/if}
      </div>
    </div>
    {#if roomId && room && hasTerminal}
      {#key room.id}<TermDock roomId={room.id} session={drill && !drill.rootSeat ? drill.id : ''} owner={room.repo ? base(room.repo) : room.name} noRepo={!room.repo} {narrow} request={dockRequest} review={dockReview} requests={ownerRequests(questions)} names={Object.fromEntries(members.filter((m) => m.id).map((m) => [m.id, m.name]))} />{/key}
    {/if}
  </main>
</div>
{/if}
<VoiceHost bind:this={voiceHost} bind:calls={roomCalls} {roomId} {narrow} {rooms} enabled={!halt && !needPair && !pop} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} />
{#if showPalette}
  <Palette {rooms} commands={paletteCommands} {targets} {roomId} roomName={room?.name ?? ''} initial={searchText} onmessage={jumpTo} path={palettePath} onroom={paletteRoom} ontarget={openPaletteTarget} onclose={closeSearch} />
{/if}
{#if viewing && room}
  {@const attachment = viewing}
  <Viewer roomId={viewing.roomId ?? room.id} att={viewing} onjump={attachment.postId && (attachment.roomId ?? room.id) === room.id ? () => { viewing = null; if (attachment.postId) jumpTo(attachment.postId) } : null} onclose={() => (viewing = null)} />
{/if}
{#if settingsPanel.showSettings}
  {#key settingsPanel.settingsKey}
    <Settings {update} {dataDir} start={settingsPanel.settingsAt.start} pairNow={settingsPanel.settingsAt.pairNow} profile={settingsPanel.settingsAt.profile ?? ''} accountId={settingsPanel.settingsAt.accountId ?? ''} initialSelection={settingsPanel.settingsAt.initialSelection} roomId={room?.id ?? ''} oncreateprofile={createProfile} onclose={() => { settingsPanel.closeSettings(); loadProfiles(); home?.refresh(); voiceCall.refresh() }} />
  {/key}
{/if}
{#if settingsPanel.showRoomSettings && room}
  {#key settingsPanel.roomSettingsKey}
  {#snippet roomRules()}<Rules roomId={room.id} view={rules} members={hierarchy} {ownerHue} bind:editing={goalView.rulesEditing} onchanged={loadRules} />{/snippet}
  <RoomSettings {room} {profiles} onrootprofile={(id: string, value: string) => { if (roomData.feed?.room.id === id) roomData.feed.room.rootProfile = value }} oncheckin={(id: string, value: string) => { if (roomData.feed?.room.id === id) roomData.feed.room.checkIn = value || undefined }} onlimit={(id: string, field: RoomLimitField, value: string) => { if (roomData.feed?.room.id === id) { const limits = { ...roomData.feed.room.limits }; if (value) limits[field] = value; else delete limits[field]; roomData.feed.room.limits = limits } }} start={settingsPanel.roomSettingsAt.start} hookRun={settingsPanel.roomSettingsAt.hookRun ?? null} {reviewer} onreviewer={loadReviewer} controls={roomControls} onsessions={(action: string) => controls.run(action)} onaction={(id: string) => palette.registry(room).find((a) => a.id === id)?.run()} rules={roomRules} busy={goalView.rulesEditing} onopenglobal={() => { settingsPanel.closeRoomSettings(); openSettings('hooks') }} onclose={() => (settingsPanel.closeRoomSettings())} />
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
  <Confirm title="Clear dormant sessions?" lines={[`Close ${context.count} dormant session${context.count === 1 ? '' : 's'}?`, 'Dormant sessions finished with no open work. A session that gained work since then stays open. Transcripts stay available under closed sessions. Clean merged worktrees are removed; unmerged or dirty worktrees are kept and reported.']} action="Clear dormant sessions" onconfirm={() => clearRoomDormant(context)} oncancel={controls.dismiss} />
{/if}
{#if selectionMenu}
  {@const context = selectionMenu}
  <Menu x={context.x} y={context.y} title={`${context.ids.length} selected`} items={selectionItems(context)} onclose={closeSelectionMenu} />
{/if}
{#if menu}
  <Menu x={menu.x} y={menu.y} title={menu.m.address} items={menuItems(menu.m, $expedite.goalId)} onclose={closeMenu} />
{/if}
{#if currentBulk?.value && currentBulk.roomId === roomId}
  {@const batch = { ...currentBulk, value: currentBulk.value }}
  {#key batch.key}<BulkSessionAction {batch} members={hierarchy} onclose={closeBulk} onrun={(force) => runBulk(batch.key, force)} onconfigure={(patch) => configureBulk(batch.key, patch)} onreload={() => loadBulk(batch.key)} onselectfailed={() => selectBulkFailures(batch)} />{/key}
{/if}
{#if consolidating && room && consolidating.roomId === roomId}
  {@const context = { ...consolidating }}
  {#key context.key}<SessionConsolidation roomId={context.roomId} sessions={context.sessions} onoperation={(operation) => consolidationChanged(context, operation)} onclose={closeConsolidation} onopen={(id) => { const m = hierarchy.find((m) => m.id === id); closeConsolidation(); if (m) openSession(m) }} />{/key}
{/if}
{#if transferring && room}
  <ModelTransfer member={transferring} roomId={room.id} {profiles} onclose={() => { sessionActions.setTransferring(null); loadMembers(); if (drill) loadSession() }} />
{/if}
{#if expediting && room}
  {@const context = { ...expediting }}
  <ExpediteGoal goals={context.goals} roomId={room.id} session={context.member.address} expeditedId={$expedite.goalId} onpick={(g: Goal) => { sessionActions.setExpediting(null); expediteGoal(g).catch((e) => say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) })) }} onstop={() => { sessionActions.setExpediting(null); stopExpediting(context.goals) }} onclose={() => (sessionActions.setExpediting(null))} />
{/if}
{#if confirm && confirmText}
  <Confirm {...confirmText} busy={confirm.busy} error={confirm.error} onconfirm={() => runConfirm()} oncancel={dismiss} />
{/if}
{#if starting && room}
  <RunWorkflow {room} onstarted={runStarted} oncancel={() => (starting = false)} />
{/if}
{#if roomActions.prompt}
  <Prompt {...roomActions.prompt} onsubmit={roomActions.prompt.submit} oncancel={roomActions.dismiss} />
{/if}
{#if closeResult}
  <CloseResult name={closeResult.name} outcomes={closeResult.outcomes} onclose={() => (closeResult = null)} onchoose={chooseCloseRow} />
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
  <ProfileModal {goals} older={listView.pages.goals?.older} loadingOlder={listView.olderLoading} onolder={() => loadOlderLists(['goals'])} ongoal={(id) => { card = null; openGoal(id) }} onpost={jumpTo} onopenfile={(a: Attachment & { roomId?: string; postId?: string }) => (viewing = a)} {profile} error={profileError} {room} ondetail={openDetail} onclose={() => (card = null)} onmessage={openSession} onmention={mentionMember} />
{/if}

<style>
  .conversation-workspace { flex: 1; min-height: 0; min-width: 0; display: flex; position: relative; }
  .sess-wrap { position: relative; flex: 1; min-height: 0; min-width: 0; display: flex; flex-direction: column; }
  .app { display: flex; height: 100vh; height: var(--app-height, 100dvh); min-height: 0; }
  .panel { display: contents; }
  .scrim { display: none; }
  .main { flex: 1; display: flex; flex-direction: column; min-width: 0; background: var(--main); position: relative; }
  .content { flex: 1; display: flex; min-height: 0; }
  .center { flex: 1; display: flex; flex-direction: column; min-width: 0; position: relative; }
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

  /* A phone: one column. The feed takes the width, and the members or pins
     panel slides in as a sheet from the right. */
  @media (max-width: 760px) {
    .app { padding-top: env(safe-area-inset-top); }
    .panel:has(> :global(*)) {
      display: flex;
      position: fixed;
      z-index: 45;
      top: 0;
      height: var(--app-height, 100dvh);
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
    .center { padding-bottom: env(safe-area-inset-bottom); }
    .toast-details { bottom: calc(146px + env(safe-area-inset-bottom)); max-height: 54vh; }
  }
</style>
