<script>
  import { actions, actionItems, roomMenu as roomActionItems } from './lib/ui/actions.js'
  import { createReloadGate, listVisible, reconcileRows } from './lib/ui/reloadGate.js'
  import Skeleton from './lib/ui/Skeleton.svelte'
  import { untrack } from 'svelte'
  import { fade } from 'svelte/transition'
  import { FAST, motion } from './lib/ui/motion.js'
  import { viewIdentity } from './lib/ui/viewIdentity.js'
  import { createReadMarker } from './lib/rooms/readMarker.js'
  import { mergeFeedPage, rerouteAnchors, withChannels } from './lib/rooms/feedPages.js'
  import { createRoomData } from './lib/rooms/roomData.js'
  import { createSessionData } from './lib/sessions/sessionData.ts'
  import { mergeListPage } from './lib/ui/listPages.js'
  import { hasItemChange, patchListRows } from './lib/ui/listChanges.js'
  import { reloadsUnread } from './lib/ui/unreadReload.ts'
  import { createWorkflowData } from './lib/workflows/workflowData.ts'
  import { onMount, tick } from 'svelte'
  import { api, onEvent, engineCheck, openExternal, copyText, onUnauthorized, logout } from './api.js'
  import RoomRail from './lib/rooms/RoomRail.svelte'
  import Sidebar from './lib/rooms/Sidebar.svelte'
  import CallButton from './lib/voice/CallButton.svelte'
  import VoiceWidget from './lib/voice/VoiceWidget.svelte'
  import { voiceCall, callRequests } from './lib/voice/voiceCall.ts'
  import { callLink, callTarget } from './lib/voice/callRequests.ts'
  import { GENERAL, VOICE, channelView, isChannel, inChannel, homeChannel, channelModel, parentTrail, prepareChannelText } from './lib/rooms/channels.js'
  import { isShellResult } from './lib/rooms/feedRows.js'
  import { mergeSessionPage } from './lib/sessions/sessionPages.ts'
  import { trimFeed } from './lib/rooms/feedWindow.js'
  import LookMenu from './lib/rooms/LookMenu.svelte'
  import { look, revealed, reveal as revealPost } from './lib/rooms/look.svelte.js'
  import { visibleTo, scopeOf } from './lib/rooms/look.ts'
  import Feed from './lib/rooms/Feed.svelte'
  import Composer from './lib/rooms/Composer.svelte'
  import Members from './lib/rooms/Members.svelte'
  import { createRoomSessionControls } from './lib/rooms/roomSessionControls.js'
  import Pins from './lib/rooms/Pins.svelte'
  import FeedFind from './lib/rooms/FeedFind.svelte'
  import SearchIcon from './lib/ui/SearchIcon.svelte'
  import SyncBadge from './lib/rooms/SyncBadge.svelte'
  import { remoteList, syncDelay } from './lib/rooms/sync.ts'
  import Issues from './lib/rooms/Issues.svelte'
  import Library from './lib/library/Library.svelte'
  import CIHealth from './lib/cihealth/CIHealth.svelte'
  import Questions from './lib/rooms/Questions.svelte'
  import Visuals from './lib/rooms/Visuals.svelte'
  import Services from './lib/services/Services.svelte'
  import { questionID, visualKey } from './lib/rooms/questions.js'
  import { setVisuals } from './lib/files/visuals.svelte.js'
  import { changesVisuals } from './lib/files/visuals.js'
  import Goals from './lib/rooms/Goals.svelte'
  import GoalDetail from './lib/rooms/GoalDetail.svelte'
  import { expedite, expediteScope, expediteAction, heldGoals } from './lib/rooms/expedite.ts'
  import Rules from './lib/rooms/Rules.svelte'
  import { nextRules } from './lib/rooms/rules.js'
  import PinIcon from './lib/ui/PinIcon.svelte'
  import { isFinished } from './lib/rooms/memberOrder.js'
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
  import FileBrowser from './lib/files/FileBrowser.svelte'
  import Changes from './lib/files/Changes.svelte'
  import SessionTabs from './lib/sessions/SessionTabs.svelte'
  import UseIcons from './lib/sessions/UseIcons.svelte'
  import ShellGrid from './lib/sessions/ShellGrid.svelte'
  import { runningShells } from './lib/sessions/shells.ts'
  import { trackChanges, changedCount } from './lib/sessions/changesCount.ts'
  import Tabs from './lib/ui/Tabs.svelte'
  import { rowOf, visibleTabs } from './lib/rooms/workViews.ts'
  import SessionMedia from './lib/sessions/SessionMedia.svelte'
  import ComputerPane from './lib/computers/ComputerPane.svelte'
  import Computers from './lib/computers/Computers.svelte'
  import ComputerPopout from './lib/computers/ComputerPopout.svelte'
  import ComputerPreview from './lib/computers/ComputerPreview.svelte'
  import { previewVisible, inUse } from './lib/computers/preview.js'
  import { applyEvent, pushAction, openedApps, popOut } from './lib/computers/computers.js'
  import Terminal from './lib/sessions/Terminal.svelte'
  import { available as hasTerminal } from './lib/sessions/TermPane.svelte'
  import Jobs from './lib/workflows/Jobs.svelte'
  import Prompt from './lib/ui/Prompt.svelte'
  import RunWorkflow from './lib/workflows/RunWorkflow.svelte'
  import PRPanel from './lib/prs/PRPanel.svelte'
  import GitHub from './lib/prs/GitHub.svelte'
  import RoomSettings from './lib/rooms/RoomSettings.svelte'
  import { prsBySession, isOpen, withoutReviewers } from './lib/prs/pr.ts'
  import { boot, watch, registerWorker } from './lib/settings/link.ts'
  import { engineWatch, browserEngine } from './lib/engineStatus.ts'
  import { loadLayout, saveLayout, lastRoom, saveLastRoom } from './lib/ui/prefs.ts'
  import { dmView } from './lib/sessions/dm.ts'
  import { roomLabel } from './lib/ui/format.js'
  import { parseDetailHref } from './lib/ui/details.js'
  import { prHash, parsePRHash, parseIssueHash, parseFileHash } from './lib/prs/prlink.ts'
  import { ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute } from './lib/ui/route.ts'
  import { paletteTargets } from './lib/ui/paletteTargets.ts'
  import { activeAgents, agentsHeader, roomsWorking } from './lib/rooms/agents.js'
  import { roomIDPattern as ROOM, postIDPattern as POST } from './lib/ui/posts.js'

  let rooms = $state([])
  // started turns on once the first room is chosen. Before that, the
  // empty room is not the owner's choice and must not overwrite the saved one.
  let started = $state(false)
  let roomId = $state('')
  let voiceWidget = $state()
  let callPush = $state(null) // the call request a push tap opened
  let roomCalls = $derived($callRequests.filter((r) => r.room === roomId))
  // The room's whole history of call requests, for its feed rows.
  $effect(() => {
    if (roomId) callRequests.load(roomId).catch(() => {})
  })
  const acceptCall = (req) => voiceWidget?.accept(req)
  const declineCall = (req) => voiceWidget?.decline(req)
  let pageVisible = $state(document.visibilityState === 'visible')
  // agentsNav: the rail's agents tile is open, so the sidebar lists the awake
  // agents of every room. Picking one opens its room and keeps the list.
  let agentsNav = $state(false)
  let agentMembers = $state(null) // room id -> members, for the agents list; null until first loaded
  let agentsRequest = 0
  let agentGoals = $state({}) // room id -> goals, to badge expedited agents in every listed room
  function agentsVisible() { return pageVisible && agentsNav && (narrow ? navOpen : showSide) }
  const reloadAgents = createReloadGate(() => ({ room: 'agents', visit: 0 }), agentsVisible)
  let home = $state() // the home screen, which takes live events
  let agents = $derived(activeAgents(rooms, agentMembers))
  // The rail count comes from room summaries. Only the active-agents
  // view needs member lists from every room.
  let workingAgents = $derived(roomsWorking(rooms))
  $effect(() => { if (agentsVisible()) loadAgents(rooms) })
  function loadAgents(list) { return reloadAgents('members', async (current) => {
    const request = ++agentsRequest
    const open = list.filter((r) => !r.archived && r.members > 0)
    const lists = await Promise.all(open.map((r) => api.members(r.id).catch(() => null)))
    const goalLists = await Promise.all(open.map((r) => api.goals(r.id).then((page) => page?.items ?? [], () => null)))
    if (!current() || request !== agentsRequest) return
    agentGoals = Object.fromEntries(open.map((r, i) => [r.id, goalLists[i] ?? agentGoals[r.id] ?? []]))
    agentMembers = Object.fromEntries(open.map((r, i) => [r.id, reconcileRows(agentMembers?.[r.id] ?? [], lists[i] ?? agentMembers?.[r.id] ?? [])]))
  }) }
  let feed = $state(null)
  let feedAtBottom = $state(true)
  let members = $state([])
  let hierarchy = $state([])
  let choices = $state([])
  let view = $state(GENERAL)
  let prLink = $state(null) // a PR link from the address bar, for the Pull requests view
  let unreadFrom = $state('')
  let drill = $state(null) // the member whose transcript is open
  let readOnlyDm = $state(false) // the open transcript came from Services: no header tabs, no composer
  let session = $state(null)
  let sessionReadingHistory = $state(false)
  let threadCount = $derived(dmView(session, room || { name: '' }).threads.length)
  let sessionTurn = $state(0) // a completion link opens its exact stored turn
  let openJob = $state('')
  let sessionVisit = 0
  let detailRequest = 0
  let roomRequest = 0 // room data can still load after a later view choice
  let queue = $state([]) // steering that waits for the open session's turn
  let sessionError = $state('')
  const layout = loadLayout({ side: true, members: true, threads: true })
  let threadsOpen = $state(layout.threads)
  let selectedThread = $state('')
  let contextOpen = $state(false)
  let listPages = $state({})
  let olderLoading = $state(false)
  let olderFailed = $state(false) // the last older-page load failed
  let jobs = $state(null) // the room's shell jobs, newest first: one list for every view
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
  let halt = $state(null) // {key} or {error}: Connect stopped the app before it started
  let route = $state(null) // the route this page uses, for the sidebar
  let showClosed = $state(false)
  let showPins = $state(false) // the Pinned panel takes the member list's place
  let finding = $state(false) // the find bar under the room header
  let feedFind = $state()
  let questions = $state([])
  let visuals = $state([])
  let questionFocus = $state('')
  let questionFocusRequest = $state(0)
  let questionsRequest = 0
  let pins = $state([]) // the room's pinned posts, newest pin first
  let syncStatus = $state({}) // saved remote counts by repo path
  let syncRepo = $derived(syncStatus[room?.repo] ?? null) // {remotes, integration, operation?}
  let syncRemotes = $derived(remoteList(syncRepo?.remotes))
  let syncNote = $state('') // the last Refresh or Export answer that was not a start
  let showIssues = $state(false) // the Issues panel, beside Pinned; it needs an open issue
  let channelMenu = $state(null) // {x, y}: a phone shows the panel toggles in this menu
  let lookOpen = $state(false) // a phone opens the channel look from that menu
  let issues = $state([]) // open issues, newest first
  let issueHistory = $state([]) // closed and converted issues, newest first
  let feedView = $state()
  let pinnedIds = $derived(new Set(pins.map((p) => p.id)))
  let reply = $state(null)
  let drafts = $state({})
  let dataDir = $state('')
  let loadError = $state('')
  let health = $state('')
  let engineStatus = $state('') // why the event stream is down, see engineWatch
  let composer = $state()
  let card = $state(null) // the member whose profile modal is open
  let profile = $state(null)
  let profileError = $state('')
  let sessionView = $state()
  let viewing = $state(null) // the attachment open in the viewer; roomId names its room when it is not this one (a call's shared file)
  let dropping = $state(false) // files are being dragged over the window
  let profiles = $state([]) // what "/" completes in the composer
  let fresh = $state('') // a session just started from the composer: its row flashes
  let menu = $state(null) // {m, x, y, anchor}: the open session menu
  let roomMenu = $state(null) // {r, x, y, anchor}: the open room menu
  let goals = $state([])
  let goalId = $state('')
  // The room's one expedited goal, kept for the badges. A timer wakes it at the
  // deadline, so the badges leave on time without a server event.
  let expireTick = $state(Date.now())
  let knownGoals = $derived(fetchedGoal && !goals.some((g) => g.id === fetchedGoal.id) ? [...goals, fetchedGoal] : goals)
  $effect(() => {
    const scope = expediteScope(knownGoals, hierarchy, expireTick)
    // The agents list shows other rooms too: their expedited agents carry the badge as well.
    const others = Object.entries(agentGoals).filter(([id]) => id !== roomId).map(([id, list]) => expediteScope(list, agentMembers?.[id] ?? [], expireTick))
    const agents = new Set([scope, ...others].flatMap((x) => [...x.agents]))
    expedite.set({ ...scope, agents })
    const until = Math.min(...[scope, ...others].filter((x) => x.goalId).map((x) => x.until))
    if (!Number.isFinite(until)) return
    const timer = setTimeout(() => (expireTick = Date.now()), Math.min(until - Date.now() + 50, 2 ** 31 - 1))
    return () => clearTimeout(timer)
  })
  // A goal opened by a link but held by no loaded page; list refreshes keep it.
  let fetchedGoal = $state(null)
  // Counts change events applied to fetchedGoal, so a slower reload answer
  // never replaces a newer event.
  let fetchedGoalVersion = 0
  let selectedGoal = $derived(goalById(goalId))
  // rules is the list in the Room settings Rules section; rulesPending waits while an inline edit
  // is open (rulesEditing) and replaces it when the edit closes.
  let rules = $state(null)
  let rulesPending = $state(null)
  let rulesEditing = $state(false)
  $effect(() => { if (!rulesEditing && rulesPending) { rules = rulesPending; rulesPending = null } })
  let reviewer = $state(null) // the room's reviewer (ReviewerStatus)
  let changes = $state(null) // the room's changes, local and PR (Changes); null before they load or on a host without the API
  // The room's pull requests: the Changes rows that are not local changes.
  let prs = $derived((changes ?? []).filter((c) => c.kind !== 'local'))
  let prError = $state('')
  let prMap = $derived(prsBySession(prs))
  // Agent rows show a chip only while a PR is open; the panel lists them all.
  let openPRMap = $derived(prsBySession(prs.filter(isOpen)))
  let confirm = $state(null)
  const bulkControllers = new Map()
  let bulkSerial = 0
  let activeBulk = $state(0)
  let bulkBatches = $state([])
  let selectionRevision = 0
  let currentBulk = $derived(bulkBatches.find((b) => b.key === activeBulk))
  let roomSessionController = null
  let roomControls = $state(null)
  let roomControlsMenu = $state(null)
  let clearDormantConfirm = $state(null)
  let roomControlsVisit = 0
  let selectionMenu = $state(null)
  let selectingSessions = $state(false)
  let pickedSessions = $state([])
  let consolidating = $state(null)
  let consolidationView = 0
  let consolidationAlive = true
  let transferring = $state(null) // {kind: 'stop'|'close', m, busy, error}
  let toast = $state('')
  let closeResult = $state(null)
  let runs = $state(null) // roomsvc.RunList for the room
  let runsError = $state('')
  let openRun = $state('') // the run whose detail is open, in the runs view
  let runDetail = $state(null)
  let runError = $state('')
  let showSettings = $state(false)
  let showRoomSettings = $state(false)
  let settingsKey = $state(0)
  let palettePath = $state(null) // set: the palette opens on New room with this path
  let settingsAt = $state({ start: 'profiles', pairNow: false }) // where Settings opens
  let showPalette = $state(false) // Ctrl+K, or the search button on a phone
  let update = $state(null) // configsvc.UpdateNotice: a newer dyna release
  let updateHidden = $state(false)
  let sessionTab = $state('chat') // the open tab of a session view
  let changesTracker = null // pokes a reload of the open session's changed-file count when it shows activity
  let sessionChanged = $state(0) // files changed in the open session's worktree; the Changes tab shows above zero
  $effect(() => {
    const session = drill?.id, roomRef = room?.id
    sessionChanged = 0
    if (!session || !roomRef) return
    let stale = false
    const load = () => api.gitStatus(roomRef, session).then((st) => { if (!stale) sessionChanged = changedCount(st) }, () => { if (!stale) sessionChanged = 0 })
    const tracker = trackChanges(load)
    changesTracker = tracker
    return () => { stale = true; tracker.stop(); changesTracker = null }
  })
  // The PR views appear without the view fade.
  let prView = $derived((!drill && (view === 'github' || view === 'issues')) || (!!drill && sessionTab === 'prs'))
  // Agent computers of the open room (null when the feature is unavailable),
  // each session's recent actions, and whether the viewer is open.
  let computers = $state(null)
  let computerFeed = $state({})
  let computerError = $state('')
  let winWidth = $state(typeof window === 'undefined' ? 1400 : window.innerWidth)
  const popMatch = /^#pop=(room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}):([A-Za-z0-9][A-Za-z0-9_.-]{0,95})$/.exec(typeof location === 'undefined' ? '' : location.hash)
  const pop = popMatch ? { room: popMatch[1], session: popMatch[2] } : null
  let computerRequest = 0
  function loadComputers() { return reload('computers', async (current) => {
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
  let computerTimer = 0
  function handleComputer(ev) {
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
  const computerOf = (id) => computers?.find((c) => c.session === id) ?? null
  let reveal = $state('') // a file the Files view opens, from Changes
  let revealFolder = $state(null) // a folder the Files view shows, from Library; an object so the root and repeats still open
  let starting = $state(false) // the Run workflow dialog is open
  let ask = $state(null) // {title, lines, label, value, multiline, match, action, danger, submit}: the open Prompt
  // The sidebar row of the open work view, and its tabs when it has several.
  let workRow = $derived(rowOf(view))
  let workTabs = $derived(visibleTabs(workRow))
  // The room feed is on screen: a channel view, not a work view or a session.
  let feedShown = $derived(!!room && !drill && isChannel(view) && !workRow)

  function hasFiles(e) {
    return Array.from(e.dataTransfer?.types || []).includes('Files')
  }
  function dragOver(e) {
    if (!hasFiles(e) || !room) return
    e.preventDefault()
    dropping = true
  }
  function drop(e) {
    dropping = false
    if (!hasFiles(e)) return
    e.preventDefault()
    const files = e.dataTransfer.files
    if (drill) sessionView?.addFiles(files)
    else composer?.addFiles(files)
  }

  let room = $derived(feed?.room ?? null)
  let current = $derived(rooms.find((r) => r.id === roomId))
  let channels = $derived(channelModel(hierarchy))
  let channel = $derived(view.startsWith('channel:') ? channels.byId.get(view.slice(8)) : null)
  let channelTrail = $derived(channel ? channels.trail(channel.id) : [])
  // A phone hides the older crumbs, so the current one carries the whole trail.
  let trailTitle = $derived(['general', ...channelTrail.map((a) => a.name)].join(' / '))
  // The root DM is a session view like any other; it keeps the room's voice call button.
  let isRootDm = $derived(!!drill && (drill.rootSeat || drill.role === 'root'))
  let rootSeat = $derived(hierarchy.find((m) => m.owner)?.seat ?? {})
  // The root conversation opens like any session. A root with no session of its own (an external
  // seat holder, or an internal root that has not started) is a member made from the seat; its
  // posts load through RootSession.
  function rootMember() {
    const owner = hierarchy.find((m) => m.owner)
    const holder = rootSeat.external ? (rootSeat.harness ? rootSeat.harness + ' (external)' : 'External orchestrator') : 'Internal root'
    return { id: 'root:' + roomId, rootSeat: true, role: 'root', name: 'root', address: room?.name + ':root', hue: owner?.hue ?? 0, state: 'idle', status: 'seat', profile: holder + ' holds the seat', seat: rootSeat }
  }
  // The breadcrumbs above a session and a run: root, then the parent sessions.
  let sessionTrail = $derived(drill && !readOnlyDm && !isRootDm ? parentTrail(channels, drill.parent, rootMember()).filter((m) => m.id !== drill.id) : [])
  let runTrail = $derived(view === 'runs' && openRun && runDetail?.run?.parentId ? parentTrail(channels, runDetail.run.parentId, rootMember()) : [])
  let channelLabel = $derived(channel?.name ?? 'general')
  // Shell exit records live in shell-jobs, not in channel feeds.
  let posts = $derived((feed?.posts ?? []).filter((p) => !isShellResult(p) && (!isChannel(view) || inChannel(p, view))))
  // What the owner chose to show in this channel; hidden posts still count as read.
  let shownPosts = $derived(isChannel(view) ? posts.filter(visibleTo(look, scopeOf(roomId, view), revealed)) : posts)
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
  const computerDraft = (v) => {
    const key = 'computer:' + roomId + ':' + drill?.id
    if ((drafts[key] ?? '') !== v) drafts = { ...drafts, [key]: v }
  }

  let memberChips = $derived(showMembers && !showPins && !drill)
  let sidebarChips = $derived((narrow ? navOpen : showSide) && (agentsNav || live.length > 0))
  function visibleList(name) {
    return consolidationAlive && pageVisible && listVisible(name, {
      view, drill, showRoomSettings, memberChips, sidebarChips,
      sideVisible: narrow ? navOpen : showSide,
      controlsOpen: !!roomControlsMenu || !!clearDormantConfirm,
    })
  }
  const reload = createReloadGate(() => ({ room: roomId, visit: roomRequest }), visibleList)
  const readMarker = createReadMarker(api, {
    current: () => ({ room: roomId, visit: roomRequest + ':' + view, visible: pageVisible && consolidationAlive && !drill && isChannel(view) }),
    read: () => { soonRooms(); soonRooms('feed') },
  })

  // The unread refresh carries channel counts into the open feed.
  function applyUnread(value) {
    if (feed?.room.id !== value.room.id) return
    feed.channelUnread = value.channelUnread ?? {}
  }
  const roomData = createRoomData(api, {
    current: () => ({ room: roomId, visit: roomRequest }),
    visible: visibleList,
    choices: (value) => { choices = value },
    rooms: (value) => { rooms = value; loadError = '' },
    unread: applyUnread,
    members: applyMembers,
    patchMembers: applyMemberItem,
    opened: (value, initial) => {
      if (initial) unreadFrom = value.feed.firstUnread ?? (value.feed.posts ?? []).find((p) => p.unread)?.id ?? ''
      applyOpened(value)
    },
    error: (error) => (loadError = error),
  })
  function loadRooms() { return roomData.loadRooms() }
  function loadMembers() { return roomData.loadMembers() }
  function memberRoster(list) {
    return JSON.stringify(list.filter((m) => !m.owner).map((m) => [m.id, m.name, !!m.closed]).sort())
  }
  // Channels come from the server with each post, so a parent change (a
  // promote) rereads the routing of every loaded post. reconcileRows updates
  // rows in place, so the last key is kept, not recomputed.
  let routedParents = ''
  function parentKey(list) {
    return JSON.stringify([roomId, list.filter((m) => !m.owner).map((m) => [m.id, m.parent || '']).sort()])
  }
  // noteParents records the member parent tree. When it changed in the same
  // room, loaded posts reread their channels. Live member updates and room
  // opens, including a reconnect, both come through here.
  function noteParents(rows) {
    const parents = parentKey(rows)
    const moved = routedParents !== parents && JSON.parse(routedParents || '[null]')[0] === roomId
    routedParents = parents
    if (moved) rerouteLoaded()
  }
  async function rerouteLoaded() {
    const id = roomId, request = roomRequest
    if (!feed?.posts.length) return
    try {
      const pages = await Promise.all(rerouteAnchors(feed.posts).map((post) => api.feedAround(id, post, 200)))
      if (id === roomId && request === roomRequest && feed) feed.posts = withChannels(feed.posts, pages)
    } catch { /* The next room open or reconnect reroutes the posts. */ }
  }
  function roomCountKey(list) {
    return JSON.stringify(list.filter((m) => !m.owner && !m.closed).map((m) => [m.id, m.state === 'live']).sort())
  }
  function applyMembers(value) {
    const changed = memberRoster(hierarchy) !== memberRoster(value)
    const counts = roomCountKey(hierarchy)
    showMemberRows(reconcileRows(hierarchy, value), changed, counts)
  }
  function applyMemberItem(change) {
    const before = memberRoster(hierarchy)
    const counts = roomCountKey(hierarchy)
    const known = hierarchy.some((m) => (m.id || m.address) === change.id)
    const rows = patchListRows(hierarchy, change, (member) => member.id || member.address)
    showMemberRows(rows, before !== memberRoster(rows), known ? counts : null)
    return hierarchy
  }
  function showMemberRows(rows, rosterChanged, previousCounts) {
    noteParents(rows)
    hierarchy = rows
    setPicked(pruneSelection(pickedSessions, hierarchy))
    members = hierarchy.filter((m) => showClosed || !m.closed)
    view = channels.resolveView(view)
    if (rosterChanged) loadComputers()
    if (previousCounts !== roomCountKey(rows)) soonRooms()
  }

  function visitRoomControls(id) {
    roomSessionController?.destroy()
    roomSessionController = null
    roomControls = null
    roomControlsMenu = null
    clearDormantConfirm = null
    const visit = ++roomControlsVisit, request = roomRequest
    if (!id) return
    roomSessionController = createRoomSessionControls(api, id, (value) => {
      if (!consolidationAlive || id !== roomId || request !== roomRequest || visit !== roomControlsVisit) return
      const finished = roomControls?.busy && !value.busy
      roomControls = value
      if (finished) { loadMembers(); loadChanges() }
    }, () => visibleList('roomControls'))
  }
  function roomSessionActions(x, y, anchor) {
    roomControlsMenu = { x, y, anchor, roomId, visit: roomControlsVisit }
  }
  function closeRoomSessionMenu() { const anchor = roomControlsMenu?.anchor; roomControlsMenu = null; anchor?.focus?.() }
  function currentRoomControls(context) { return consolidationAlive && context?.roomId === roomId && context.visit === roomControlsVisit }
  function askClearDormant(count) {
    if (!consolidationAlive || !roomId || roomControls?.busy) return
    const n = count || roomControls?.state?.dormant || 0
    if (!n) return
    clearDormantConfirm = { roomId, visit: roomControlsVisit, count: n }
  }
  function roomSessionItems(context) {
    const busy = !roomControls?.state || roomControls.busy
    const dormant = roomControls?.state?.dormant || 0
    return [
      { label: 'Pause all', hint: 'let running turns finish', disabled: busy || roomControls.state.paused, run: () => { if (currentRoomControls(context)) roomSessionController.run('pause') } },
      { label: 'Resume all', disabled: busy, run: () => { if (currentRoomControls(context)) roomSessionController.run('resume') } },
      { label: 'Clear dormant sessions…', gap: true, disabled: busy || !dormant, run: () => { if (currentRoomControls(context)) askClearDormant(dormant) } },
    ]
  }
  function clearRoomDormant(context) {
    if (!currentRoomControls(context) || clearDormantConfirm?.visit !== context.visit) return
    clearDormantConfirm = null
    roomSessionController.run('clearDormant')
  }

  function setPicked(ids) {
    if (JSON.stringify(ids) !== JSON.stringify(pickedSessions)) { pickedSessions = ids; selectionRevision++ }
  }
  function bulkChanged(record, value) {
    if (!consolidationAlive) return
    const old = bulkBatches.find((b) => b.key === record.key)?.value
    const next = { ...record, value }
    bulkBatches = bulkBatches.some((b) => b.key === record.key) ? bulkBatches.map((b) => b.key === record.key ? next : b) : [next, ...bulkBatches]
    if (record.roomId === roomId && record.roomRequest === roomRequest && record.selectionRevision === selectionRevision && value.started) {
      const byId = new Map(value.results.map((r) => [r.session, r]))
      setPicked(record.sessions.map((m) => m.id).filter((id) => !byId.has(id) || bulkRetryable(byId.get(id), record.action)))
      record.selectionRevision = selectionRevision
    }
    if (old?.busy && !value.busy && value.started && record.roomId === roomId && record.roomRequest === roomRequest) { loadMembers(); loadChanges() }
  }
  function openBulkAction(action, context) {
    if (!currentSelection(context)) return
    const sessions = context.ids.map((id) => hierarchy.find((m) => m.id === id)).filter(selectableSession).map((m) => ({ ...m }))
    if (!sessions.length) return
    detailRequest++
    const record = { key: ++bulkSerial, roomId, roomRequest, selectionRevision, sessions, action }
    const controller = createBulkSessionAction(api, record, (value) => bulkChanged(record, value))
    bulkControllers.set(record.key, controller)
    activeBulk = record.key
    controller.load()
  }
  function runBulk(key, force = false) { bulkControllers.get(key)?.run(force) }
  function selectBulkFailures(batch) {
    if (batch.roomId !== roomId) return
    const failed = batch.value.results.filter((r) => bulkRetryable(r, batch.action)).map((r) => r.session)
    setPicked(pruneSelection(failed, hierarchy)); selectingSessions = true
    activeBulk = 0
  }
  function selectionActions(x, y, anchor) {
    selectionMenu = { x, y, anchor, roomId, ids: [...pickedSessions], key: selectionKey(roomId, pickedSessions) }
  }
  function closeSelectionMenu() { const anchor = selectionMenu?.anchor; selectionMenu = null; anchor?.focus?.() }
  function currentSelection(context) { return context?.roomId === roomId && context.key === selectionKey(roomId, pickedSessions) }
  function selectionItems(context) {
    return [
      { label: 'Select all sessions', run: () => { if (currentSelection(context)) setPicked(people.filter(selectableSession).map((m) => m.id)) } },
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
  function moveUnder(m) {
    setPicked([m.id])
    openBulkAction('adopt', { roomId, ids: [m.id], key: selectionKey(roomId, [m.id]) })
  }
  function messageSelection(context) {
    if (!currentSelection(context)) return
    const selected = context.ids.map((id) => hierarchy.find((m) => m.id === id)).filter(selectableSession)
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
  function pickSession(m, checked, begin = false) { if (begin) selectingSessions = true; setPicked(toggleSelection(pickedSessions, m, checked)) }
  function openConsolidation() {
    const ids = pruneSelection(pickedSessions, hierarchy)
    if (ids.length < 2 || ids.length > 16) return
    detailRequest++
    const sessions = ids.map((id) => hierarchy.find((m) => m.id === id) ?? { id, name: id })
    consolidating = { roomId, sessions, key: ++consolidationView }
    menu = null
  }
  function consolidationChanged(context, operation) {
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
    api.profiles().then((p) => (profiles = p ?? [])).catch(() => {})
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

  async function pauseRun(run) {
    await api.pauseRun(run.id, !run.paused)
    say(run.paused ? `${run.name} resumed.` : `${run.name} is paused. Running agents finish; no new ones start.`)
    loadRuns()
    await loadRun()
  }
  async function stopJob(job) {
    await api.stopShell(roomId, job.id)
    say(`Shell job ${job.id} stopped.`)
    workflowData.loadJobs()
  }

  function showRun(r) {
    clearItemLink()
    detailRequest++
    drill = null
    view = 'runs'
    if (openRun !== r.id) runDetail = null
    openRun = r.id
    loadRun()
  }

  function loadQuestions() { return reload('questions', async (current) => {
    const request = ++questionsRequest
    const id = roomId
    if (!id) return
    try {
      const q = await api.questions(id)
      if (current() && id === roomId && request === questionsRequest) { questions = reconcileRows(questions, q?.[id] ?? []) }
    } catch (e) { if (current() && id === roomId && request === questionsRequest) loadError = String(e) }
  }) }
  function loadVisuals() { return reload('visuals', async (current) => {
    try {
      const list = await api.visuals(roomId)
      if (current()) {
        visuals = reconcileRows(visuals, list ?? [], visualKey)
        setVisuals(roomId, visuals)
      }
    } catch (e) { if (current()) loadError = String(e) }
  }) }

  async function answerQuestion(id, choice, text) {
    await api.answerQuestion(roomId, id, choice, text)
    await loadQuestions()
  }
  function openQuestion(post) {
    openView('questions')
    questionFocus = questionID(post)
    questionFocusRequest++
  }

  function loadGoals() { return reload('goals', async (current) => {
    const id = roomId
    if (!id) return
    try { const list = await api.goals(id); if (current() && roomId === id) { goals = applyListPage('goals', list); await refreshFetchedGoal(current) } }
    catch (e) { if (current() && roomId === id) loadError = String(e) }
  }) }

  function loadRules() { return reload('rules', async (current) => {
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
  async function openGoal(id) {
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
      if (rid === roomId) say(`Could not open goal ${id}: ${String(e?.message || e)}`)
      return null
    }
  }
  function goalById(id) {
    return goals.find((g) => g.id === id) ?? (fetchedGoal?.id === id ? fetchedGoal : undefined)
  }
  // The fetched goal follows its change events, as listed goals do.
  function trackFetchedGoal(change) {
    if (!fetchedGoal || change?.id !== fetchedGoal.id) return
    fetchedGoalVersion++
    fetchedGoal = change.op === 'delete' ? null : change.item ?? fetchedGoal
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
  function loadSync() { return reload('sync', async (current) => {
    try {
      const s = await api.syncStatus()
      if (current()) syncStatus = s ?? {}
    } catch { /* quiet */ }
  }) }

  // Refresh and Export start in the background; the saved operation shows
  // the progress. An error, such as one already running, shows in the popover.
  async function syncAction(call) {
    syncNote = ''
    try {
      await call(roomId, room.repo)
    } catch (e) {
      syncNote = String(e?.message || e)
    }
    loadSync()
  }

  function loadPins() { return reload('pins', async (current) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.pins(id)) ?? []
      if (current() && id === roomId) pins = reconcileRows(pins, list)
    } catch (e) {
      if (current()) loadError = String(e)
    }
  }) }

  function loadIssues(history = false) { return reload(history ? 'issueHistory' : 'issues', async (current) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.issues(id, history)) ?? []
      if (!current() || id !== roomId) return
      if (history) issueHistory = reconcileRows(issueHistory, list.filter((i) => i.state !== 'open'))
      else { issues = reconcileRows(issues, list); if (!issues.length) showIssues = false }
    } catch (e) {
      if (current()) loadError = String(e)
    }
  }) }

  // A slow-test notice opens the Issues panel, where its issue is listed.
  function openIssues() { showIssues = true; showPins = false; if (narrow) showMembers = false }

  // One issue action: run the call, then refresh both lists.
  async function triageIssue(call) {
    try {
      await call()
      loadError = ''
      loadIssues()
      if (showIssues) loadIssues(true)
    } catch (e) {
      say(`Could not update the issue: ${String(e?.message || e)}`)
    }
  }

  // Close needs a reason: the backend rejects an empty one.
  function askCloseIssue(i) {
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

  async function togglePin(p) {
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
  async function jumpTo(id) {
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
    if (questionID(post)) { openQuestion(post); return }
    if (post && isShellResult(post)) { openJob = post.from.slice('shell:'.length); view = 'jobs'; return }
    // A voice call line has no channel view; Voice's call widget shows it.
    if (post && inChannel(post, VOICE)) return
    view = post ? homeChannel(post) : GENERAL
    await tick()
    if (request === detailRequest && rid === roomId) feedView?.jump(id)
  }

  const sessionData = createSessionData(api, {
    current: () => ({ room: roomId, visit: roomRequest, id: drill?.id, root: !!drill?.rootSeat, selection: sessionVisit, turn: sessionTurn, visible: sessionVisible(), live: session?.member?.state === 'live' }),
    transcript: showSession,
    queue: (value) => (queue = value),
    error: (error) => (sessionError = error),
  })
  function showSession(value, older = false) {
    session = mergeSessionPage(session, value, older, sessionReadingHistory)
    sessionError = ''
    // The root seat opened before root had a session of its own. Once RootSession returns the real
    // session (root started, or the seat was released), continue as that member: its queue, paging and events.
    if (drill?.rootSeat && value.member?.id) {
      drill = value.member
      loadSession()
    }
  }
  function sessionVisible(tab = sessionTab) { return pageVisible && ['chat', 'computer', 'media'].includes(tab) }
  function loadSession() { return sessionData.load() }
  $effect(() => {
    const tab = sessionTab
    untrack(() => { if (drill && sessionVisible(tab)) loadSession() })
  })

  function loadEarlierSession(near = () => true) {
    if (!session?.earlier || sessionTurn) return Promise.resolve(false)
    return sessionData.earlier(session.cursor, near)
  }

  function loadNewerSession(near = () => true) {
    if (!session?.newer || sessionTurn) return Promise.resolve(false)
    return sessionData.newer(String(session.turns.at(-1)?.n ?? ''), near)
  }
  function presentSession() { return sessionData.present() }

  async function unqueue(q) {
    await api.deletePendingSteer(drill.id, q.id)
    queue = queue.filter((x) => x.id !== q.id)
    say('Removed. The turn will not see that message.')
  }

  async function dismissPendingPost(id, messageId) {
    await api.dismissPendingPost(id, messageId, true)
    if (drill?.id === id) queue = queue.filter((post) => post.id !== messageId)
    say('Message dismissed. History stays available.')
  }

  function markShown(list) { return readMarker.mark(list) }

  // Only messages visible in the selected channel become read.
  $effect(() => {
    if (!drill && isChannel(view)) markShown(posts)
  })

  function loadReviewer() { return reload('reviewer', async (current) => {
    const id = roomId
    if (!id) return
    const status = await api.reviewerStatus(id).catch(() => null)
    if (current() && consolidationAlive && id === roomId) reviewer = status
  }) }

  // A burst of PR events refreshes the reviewer once.
  let reviewerTimer
  function reviewerSoon() {
    clearTimeout(reviewerTimer)
    reviewerTimer = setTimeout(loadReviewer, 250)
  }

  function loadChanges() { return reload('changes', async (current) => {
    const id = roomId
    try {
      const all = await api.changes(id)
      if (!current()) return
      changes = applyListPage('changes', all)
      prError = ''
    } catch (e) { if (current()) prError = String(e?.message || e) }
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

  function applyListPage(kind, page, append = false) {
    const previous = listPages[kind]
    const merged = mergeListPage(previous, page, append)
    if (kind === 'jobs') merged.items.sort((a, b) => (b.started || '').localeCompare(a.started || ''))
    merged.items = reconcileRows(previous?.items ?? [], merged.items, kind === 'changes' ? 'key' : 'id')
    listPages = { ...listPages, [kind]: merged }
    return merged.items
  }

  function applyListItem(kind, change, rows) {
    const previous = listPages[kind] ?? { items: rows ?? [] }
    const items = patchListRows(previous.items, change, kind === 'changes' ? 'key' : 'id')
    if (kind === 'jobs') items.sort((a, b) => (b.started || '').localeCompare(a.started || ''))
    listPages = { ...listPages, [kind]: { ...previous, items } }
    return items
  }

  async function loadOlderLists(kinds) {
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
        await reload.patch(kind, async (current) => {
          const selected = listPages[kind]
          if (!current() || id !== roomId || visit !== roomRequest || !selected?.older) return
          const next = await methods[kind](id, selected.cursor)
          if (!current() || id !== roomId || visit !== roomRequest) return
          const merged = applyListPage(kind, next, true)
          if (kind === 'goals') goals = merged
          if (kind === 'changes') changes = merged
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

  async function untrackPR(pr) {
    try {
      await api.untrackPR(roomId, pr.key)
      say(`Dyna stopped watching PR #${pr.number}.`)
    } catch (e) {
      say(String(e?.message || e))
    }
    loadChanges()
  }

  // trackPR links a PR to a session from the pull-requests view.
  async function trackPR(number, m) {
    try {
      await api.trackPR(roomId, m.id, number)
      say(`Dyna watches PR #${number} for ${m.name}.`)
    } catch (e) {
      say(String(e?.message || e))
    }
    loadChanges()
  }

  // askAgent puts a new-session line in the room composer, with the room's
  // default profile, so the owner reads and edits it before sending.
  async function askAgent(task) {
    if (!profiles.length) profiles = (await api.profiles().catch(() => [])) ?? []
    const p = profiles.find((x) => x.default) ?? profiles[0]
    if (!p) return say('Add a profile in Settings first.')
    openView('all')
    draft.value = `/${p.name} ${task}`
    setTimeout(() => composer?.focus(), 0)
  }

  async function resumePR(pr) {
    try {
      await api.resumePR(roomId, pr.key)
      say(`Dyna watches PR #${pr.number} again.`)
    } catch (e) {
      say(String(e?.message || e))
    }
    loadChanges()
  }

  // applyOpened shows what api.openRoom returned for the open room.
  function applyOpened(o) {
    const f = mergeFeedPage(feed && !feedAtBottom ? { ...feed, historyLoaded: true } : feed, o.feed)
    f.posts = reconcileRows(feed?.room.id === f.room.id ? feed.posts : [], f.posts ?? [])
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
    runs = o.runs ?? null
    runsError = ''
    workflowData.loadJobs()
    loadQuestions()
  }

  // Each request adds one bounded older page. Existing posts keep their
  // objects and unread marks until the room is left.
  let earlierLoad = null
  const STEP = 50
  async function loadEarlier(direction = 'older', target = '', near = () => true) {
    const id = roomId
    const roomLoad = roomRequest
    if (earlierLoad?.id === id && earlierLoad.roomLoad === roomLoad) {
      if (earlierLoad.direction === direction) return earlierLoad.promise
      return earlierLoad.promise.then(() => id === roomId && roomLoad === roomRequest && loadEarlier(direction, target, near))
    }
    if (!feed || (direction === 'older' && !feed.earlier) || (direction === 'newer' && !feed.newer)) return false
    feedAtBottom = false
    const cursor = direction === 'older' ? feed.cursor : direction === 'newer' ? feed.posts.at(-1)?.id : target
    const request = { id, roomLoad, direction, promise: null }
    earlierLoad = request
    request.promise = (async () => {
      try {
        const page = direction === 'newer' ? await api.feedAfter(id, cursor, STEP)
          : direction === 'around' ? await api.feedAround(id, cursor, STEP)
          : await api.feedTail(id, direction === 'present' ? '' : cursor, STEP)
        if (id !== roomId || roomLoad !== roomRequest || !feed) return false
        if (!near()) return true
        if (direction === 'older' && feed.cursor !== cursor) return false
        feed = direction === 'around' ? { ...page, historyLoaded: true } : mergeFeedPage(feed, page, direction)
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
  async function selectRoom(id, startView = GENERAL) {
    clearItemLink(id, startView)
    const request = ++detailRequest
    roomRequest++
    navOpen = false
    agentsNav = !id
    if (id !== roomId) {
      showRoomSettings = false
      finding = false
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
    view = ['all', 'owner', 'turns'].includes(startView) || (startView === 'terminal' && !hasTerminal) ? GENERAL : startView
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
    showIssues = false
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
  async function openMember(m) {
    if (m?.rootSeat) { openSession(m); return }
    detailRequest++
    if (!m || m.owner) return
    card = m
    profile = null
    profileError = ''
    try {
      const p = await api.profile(m.id)
      if (card?.id === m.id) profile = p
    } catch (e) {
      profileError = String(e)
    }
  }

  // openAgent opens a session from the sidebar. An agent from the agents list
  // may live in another room; its room opens first and the list stays.
  async function openAgent(m) {
    navOpen = false
    if (m.roomId && m.roomId !== roomId) {
      const selecting = selectRoom(m.roomId)
      const request = detailRequest
      agentsNav = true
      if (!await selecting || request !== detailRequest) return
    }
    openSession(m)
  }

  // openSession drills into a member's full session view.
  function openSession(m, turn = 0, initial = null, readOnly = false) {
    if (m?.rootSeat) m = rootMember()
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
      contextOpen = false
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
  async function sendToSession(text, atts = [], forwardOf = '') {
    const res = drill.rootSeat ? await api.send(roomId, '@root ' + text, '', atts, forwardOf) : await api.sendToSession(drill.id, text, atts, forwardOf)
    openSession(drill)
    return res
  }

  function openAddress(addr) {
    if (!room || addr === room.name) {
      drill = null
      return
    }
    const m = members.find((x) => x.address === addr)
    if (m) openMember(m)
  }

  function mentionMember(m) {
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
  function startReply(p) {
    reply = p
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
  async function send(text, atts = [], forwardOf = '') {
    const sent = roomId
    const draftKey = roomId + ':' + view
    const res = await api.send(sent, text, reply?.id ?? '', atts, forwardOf)
    if (res?.kind === 'spawn') {
      drafts = { ...drafts, [draftKey]: '' }
      fresh = res.spawn.member.id
      setTimeout(() => { if (fresh === res.spawn.member.id) fresh = '' }, 4000)
      loadMembers()
    } else if (res?.post?.message?.id) {
      reply = null
      // Keep the recipients so the next post goes to the same members.
      drafts = { ...drafts, [draftKey]: res.post.sticky || '' }
    }
    return res
  }

  let reopening = $state('')
  async function reopenSession(m) {
    if (reopening || dismissing || !m.closed || !m.resumable) return
    const rid = roomId, visit = roomRequest, selection = sessionVisit
    reopening = m.id
    try {
      const member = await api.reopenSession(rid, m.id)
      if (rid !== roomId || visit !== roomRequest) return
      if (drill?.id === m.id && selection === sessionVisit) {
        drill = member
        if (session?.member?.id === m.id) session = { ...session, member }
        await loadSession()
      }
      await loadMembers()
      say(`${m.name} is open. Send a message to start its next turn.`)
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
      say(`Could not take back the root seat: ${String(e?.message || e)}`)
      return
    }
    if (rid === roomId) await loadMembers()
  }
  async function dismissSession(m) {
    if (dismissing || reopening || !isFinished(m)) return
    const rid = roomId, visit = roomRequest, selection = sessionVisit
    dismissing = m.id
    try {
      const member = await api.dismissSession(rid, m.id)
      if (rid !== roomId || visit !== roomRequest) return
      if (drill?.id === m.id && selection === sessionVisit) {
        drill = member
        if (session?.member?.id === m.id) session = { ...session, member }
        await loadSession()
      }
      await loadMembers()
      say(`${m.name} is dismissed and closed. Its transcript stays available under closed sessions.`)
    } finally { dismissing = '' }
  }

  // Expedite gives the goal's agents and PRs the first review and merge.
  // A goal that would replace another asks first.
  async function expediteGoal(g) {
    const action = expediteAction($expedite, g)
    if (action === 'replace') { confirm = { kind: 'expedite', goal: g }; return }
    await api.expediteGoal(roomId, action === 'stop' ? '' : g.id, false)
    say(action === 'stop' ? `Goal ${g.id} is no longer expedited.` : `Goal ${g.id} is expedited.`)
    loadGoals()
  }
  async function linkPRGoal(change, goalId) {
    await api.setPRGoal(roomId, change.key, goalId)
    await loadChanges()
  }
  // Menu items for the goal an agent holds, or its nearest ancestor holds; with several goals, one item for each.
  function expediteItems(m) {
    if (m.closed) return []
    const held = heldGoals(m, hierarchy, knownGoals)
    return held.map((g) => ({
      label: expediteAction($expedite, g) === 'stop' ? `Stop expediting${held.length > 1 ? ` ${g.id}` : ' goal'}` : held.length > 1 ? `Expedite goal ${g.id}: ${g.text.slice(0, 40)}` : 'Expedite goal',
      run: () => expediteGoal(g).catch((e) => say(String(e?.message || e))),
    }))
  }

  // Session menu: safe actions first, then the two that stop work, set
  // apart in coral. Both of those ask first.
  function menuItems(m) {
    const items = [
      { label: m.closed ? 'Open transcript' : 'Message', run: () => openSession(m) },
      { label: 'Open profile', run: () => openMember(m) },
    ]
    if (isFinished(m)) items.push({ label: dismissing === m.id ? 'Dismissing…' : 'Dismiss', run: () => dismissSession(m).catch((e) => say(`Could not dismiss ${m.name}: ${String(e?.message || e)}`)) })
    if (m.closed && m.resumable) items.push({ label: reopening === m.id ? 'Reopening…' : 'Reopen', run: () => reopenSession(m).catch((e) => say(`Could not reopen ${m.name}: ${String(e?.message || e)}`)) })
    // The internal root holds the root seat, so it is never consolidated.
    if (!m.closed && m.role !== 'root') items.push({ label: 'Consolidate sessions…', run: () => { selectingSessions = true; setPicked([m.id]); showMembers = true; showPins = false } })
    if (!m.closed) items.push({ label: 'Mention in composer', run: () => mentionMember(m) })
    items.push(...expediteItems(m))
    items.push({ label: 'Copy address', hint: m.address, run: () => copyText(m.address).then(() => say(`Copied ${m.address}`), (e) => say(String(e))) })
    if (!m.closed) items.push({ label: m.transfer ? 'Model transfer…' : 'Transfer to new model…', run: () => { loadProfiles(); transferring = m } })
    if (!m.closed && m.parent) items.push({ label: 'Promote to lead', run: () => (confirm = { kind: 'promote', m }) })
    if (selectableSession(m)) items.push({ label: 'Move under…', run: () => moveUnder(m) })
    if (m.state === 'live') items.push({ label: 'Stop turn', danger: true, gap: true, run: () => (confirm = { kind: 'stop', m }) })
    if (!m.closed) items.push({ label: 'Close session', danger: true, gap: m.state !== 'live', run: () => (confirm = { kind: 'close', m }) })
    return items
  }

  // Room menu: everyday actions first, then Delete in coral, which asks for
  // the room's name.
  function roomMenuItems(r) {
    return roomActionItems(actionRegistry(r))
  }

  // The phone bar's overflow: the panel toggles and the channel look, which
  // the desktop bar shows as separate buttons.
  function channelMenuItems() {
    const items = [
      { label: 'Members', run: () => { showMembers = true; showPins = false; showIssues = false } },
      { label: 'Pinned posts', hint: pins.length ? String(pins.length) : '', run: () => { showPins = true; showMembers = false; showIssues = false } },
    ]
    if (issues.length) items.push({ label: 'Issues', hint: String(issues.length), run: () => { showIssues = true; showPins = false; showMembers = false } })
    if (isChannel(view)) items.push({ label: 'Channel look', gap: true, run: () => (lookOpen = true) })
    return items
  }

  async function roomAction(fn, done) {
    try {
      await fn()
      say(done)
      await loadRooms()
    } catch (e) {
      say(String(e?.message || e))
    }
  }

  function renameRoom(r) {
    ask = {
      title: 'Rename room',
      label: 'Name',
      value: roomLabel(r),
      lines: [`The new name shows in the app. Agents still address the room as #${r.name}.`],
      action: 'Rename',
      submit: async (name) => {
        await api.renameRoom(r.id, name)
        ask = null
        say(`Renamed to ${name}.`)
        loadRooms()
      },
    }
  }

  function deleteRoom(r) {
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
        say(`${roomLabel(r)} is deleted.`)
        if (roomId === r.id) selectRoom('')
        loadRooms()
      },
    }
  }

  function steerAgent(run, a) {
    ask = {
      title: `Steer ${a.label || 'agent ' + a.id}`,
      lines: [`This message goes to this agent of ${run.name} while it works.`],
      multiline: true,
      placeholder: 'What should it do differently?',
      action: 'Send',
      submit: async (text) => {
        await api.steerRun(run.id, a.id, text)
        ask = null
        say('Steering sent.')
      },
    }
  }

  async function runStarted(id) {
    starting = false
    say('Workflow started.')
    await loadRuns()
    if (id) showRun({ id })
  }

  function closeMenu() {
    const anchor = menu?.anchor
    menu = null
    anchor?.focus?.()
  }

  let toastTimer
  function say(text) {
    toast = text
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toast = ''), 3200)
  }

  // guardItems lists what a close-guard error names: "responsibilities: cannot close:\n- ...".
  function guardItems(msg) {
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
        lines: [...confirm.items, 'Some work could not move. Retry after fixing the error, or force close and leave that work unresolved.'],
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
    const lines = ['The session closes, and its name becomes free.', 'PR authorship and held goals move to its nearest open ancestor, or root. Reviewer roles return to the pool. Live workflows are canceled.']
    if (m.state === 'live') lines.push('Its running turn stops first.')
    const hasChildren = members.some((child) => !child.closed && child.parent === m.id)
    if (hasChildren) lines.push('Its open children that own no work close too. Children that still own work move to its nearest open ancestor and keep running.')
    return { title: `Close ${m.name}?`, lines, action: 'Close session' }
  })

  async function runConfirm() {
    const { kind, m } = confirm
    confirm = { ...confirm, busy: true, error: '' }
    if (kind === 'expedite') {
      try {
        await api.expediteGoal(roomId, confirm.goal.id, true)
        say(`Goal ${confirm.goal.id} is expedited.`)
        confirm = null
        loadGoals()
      } catch (e) {
        confirm = { ...confirm, busy: false, error: String(e?.message || e) }
      }
      return
    }
    if (kind === 'cancelrun') {
      try {
        await api.cancelRun(confirm.run.id)
        say(`${confirm.run.name} is canceled.`)
        confirm = null
        loadRuns()
        loadRun()
      } catch (e) {
        confirm = { ...confirm, busy: false, error: String(e?.message || e) }
      }
      return
    }
    try {
      if (kind === 'stop') await api.stopSession(roomId, m.id)
      else if (kind === 'promote') await api.promoteSession(roomId, m.id)
      else {
        const result = await api.closeSession(roomId, m.id, kind === 'forceclose')
        closeResult = { name: m.name, items: result?.closeSummary ?? [] }
      }
      confirm = null
      say(kind === 'stop' ? `Turn stopped. ${m.name} stays open.` : kind === 'promote' ? `${m.name} is now a top-level lead.` : `${m.name} is closed.`)
      loadMembers()
      loadChanges()
    } catch (e) {
      const msg = String(e?.message || e)
      // Force is offered only when the automatic handoff fails.
      if (kind === 'close' && msg.startsWith('responsibilities:')) confirm = { kind: 'forceclose', m, items: guardItems(msg) }
      else confirm = { ...confirm, busy: false, error: msg }
    }
  }

  let roomTimer
  const roomRefreshes = new Set()
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
    if (drill && sessionVisible()) loadSession()
  }

  // resync reloads what the app shows after the web event stream was lost:
  // events may have been missed while it was down.
  async function resync() {
    loadRooms()
    if (!roomId) return
    // Keep loaded history on screen when the event stream reconnects.
    if (!await roomData.open(Math.max(feed?.posts.length ?? 0, 50) + 20)) return
    roomSessionController?.load()
    if (drill) { loadSession(); changesTracker?.poke() } // events may have been missed while the stream was down
    if (openRun) loadRun()
    loadChanges()
    loadReviewer()
    loadGoals()
    loadRules()
    loadVisuals()
  }

  // A push (or a link to #room=<id>&post=<id>) names a room to open. Only
  // ids in the expected shape get through.
  async function openTarget(id, post, startView = GENERAL) {
    if (!ROOM.test(id || '') || !rooms.some((r) => r.id === id)) return false
    const selecting = selectRoom(id, startView)
    const request = detailRequest
    if (!await selecting || request !== detailRequest || id !== roomId) return false
    if (POST.test(post || '')) jumpTo(post)
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
    // The PR, Issues and Files views follow their own links with Back and Forward.
    if (id === roomId && ((pr && view === 'github') || (issue && view === 'issues') || (file && view === 'files'))) return true
    const r = parseRoute(location.hash)
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

  function applyItemEvent(ev) {
    if (!hasItemChange(ev.change) || !roomId || ev.roomId !== roomId) return false
    if (ev.kind === 'members') {
      roomData.patchMembers(ev.change)
      return true
    }
    // The PR rows come from Changes; a prs event repeats them.
    if (ev.kind === 'prs') return true
    if (ev.kind === 'changes') {
      reload.patch('changes', (current) => {
        if (current()) changes = applyListItem('changes', ev.change, changes)
      })
      return true
    }
    if (ev.kind === 'jobs') {
      workflowData.patchJobs(ev.change)
      return true
    }
    if (ev.kind === 'questions') {
      reload.patch('questions', (current) => {
        if (current()) questions = patchListRows(questions, ev.change)
      })
      return true
    }
    if (ev.kind === 'goals') {
      reload.patch('goals', (current) => {
        if (!current()) return
        goals = applyListItem('goals', ev.change, goals)
        trackFetchedGoal(ev.change)
      })
      return true
    }
    return false
  }

  function handle(ev) {
    const item = applyItemEvent(ev)
    home?.event(ev)
    if (ev.kind === 'computer') return handleComputer(ev)
    if (ev.kind === 'resync') return resync()
    if (ev.kind === 'questions' && ev.roomId === roomId && !item) loadQuestions()
    if (reloadsUnread(ev, roomId, item)) soonRooms('feed')
    if (ev.kind === 'posts' && ev.roomId === roomId) {
      if (changesVisuals(ev)) loadVisuals()
      if (roomSessionController && (roomControls?.loading || roomControls?.state?.paused !== false)) roomSessionController.load()
    }
    if (ev.kind === 'posts' && feed && ev.roomId === feed.room.id) {
      const seen = new Set(feed.posts.map((p) => p.id))
      const add = (ev.posts ?? []).filter((p) => !seen.has(p.id))
      if (add.length) {
        if (feed.newer || !feedAtBottom) feed = { ...feed, newer: (feed.newer ?? 0) + add.length }
        else {
          feed.posts = [...feed.posts, ...add]
          if (feedAtBottom) feed = trimFeed(feed)
        }
      }
    }
    if (ev.kind === 'goals' && ev.roomId === roomId) { if (!item) loadGoals(); loadRules() }
    if (ev.kind === 'goals' && ev.roomId !== roomId && agentsVisible()) loadAgents(rooms)
    if (ev.kind === 'pins' && ev.roomId === roomId) loadPins()
    if (ev.kind === 'issues' && ev.roomId === roomId) { loadIssues(); if (showIssues) loadIssues(true) }
    if (['room.paused', 'room.resumed'].includes(ev.kind) && (!ev.roomId || ev.roomId === roomId)) roomSessionController?.load()
    // A prs event without a row says the room's PR settings changed.
    if ((ev.kind === 'prs' || ev.kind === 'changes') && (!ev.roomId || ev.roomId === roomId)) {
      reviewerSoon()
      if (ev.kind === 'changes' && !item) loadChanges()
    }
    if (ev.kind === 'session' && ev.roomId === roomId && ev.change?.id === drill?.id) { loadSession(); changesTracker?.poke() }
    if (ev.kind === 'members' && ev.roomId === roomId) {
      if (!item) loadMembers()
      if (ev.change?.controls) soonRooms('controls')
    }
    if (ev.kind === 'posts' && drill && ev.roomId === roomId) {
      sessionData.schedule()
    }
    if (['goals', 'questions', 'prs', 'jobs', 'runs'].includes(ev.kind) && (!ev.roomId || ev.roomId === roomId)) soonRooms('members')
    if (ev.kind === 'jobs' && ev.roomId === roomId && !item) loadJobs()
    if (ev.kind === 'runs' && ev.roomId === roomId) {
      loadRuns()
      if (openRun) loadRun()
    }
    // Only these events change room names, unread posts or session counts.
    if ((ev.kind === 'members' && !item) || ['rooms', 'posts', 'room.paused', 'room.resumed'].includes(ev.kind)) soonRooms()
  }

  // Ctrl+B toggles the room sidebar, as in most editors. It works from the
  // composer too; a textarea has no use for the key.
  function shortcut(e) {
    if (e.key === 'Escape' && (e.defaultPrevented || e.target?.closest?.('[role="menu"], [role="dialog"], [role="alertdialog"]'))) return
    if (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      showPalette = !showPalette
      return
    }
    // Ctrl+F finds in the whole room while its feed shows. In the find box, in
    // another view, or under a dialog or menu, the browser's own find takes the key.
    if (e.key.toLowerCase() === 'f' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && feedShown && !showPalette && !e.target?.closest?.('.find, [role="menu"], [role="dialog"], [role="alertdialog"]')) {
      e.preventDefault()
      openFind()
      return
    }
    if (e.key.toLowerCase() === 'b' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      showSide = !showSide
    }
    if (e.key === 'Escape' && selectingSessions) { e.preventDefault(); toggleSessionSelection(); return }
    if (e.key === 'Escape' && drill && contextOpen && !viewing && !card && !showPalette) { contextOpen = false; return }
    if (e.key === 'Escape' && drill && threadsOpen && !viewing && !card && !showPalette) { threadsOpen = false; return }
    // Esc closes the panel unless a modal on top of it owns the key.
    if (e.key === 'Escape' && goalId && !viewing && !card && !showPalette) goalId = ''
    if (e.key === 'Escape' && showPins && !viewing && !card && !showPalette) showPins = false
    if (e.key === 'Escape' && showIssues && !viewing && !card && !showPalette) showIssues = false
    if (e.key === 'Escape' && narrow) {
      navOpen = false
      showMembers = false
    }
  }

  // The find bar focuses its box when it opens; an open bar takes focus
  // again. A query, as from /find, replaces the text.
  let findText = $state('')
  function openFind(query = '') {
    // The palette and /find can ask from another view; find searches the feed.
    if (!feedShown) openView(GENERAL)
    findText = query
    if (finding) feedFind?.focus(query)
    finding = true
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
    find: 'M8.8 14.1a5.3 5.3 0 1 0 0-10.6 5.3 5.3 0 0 0 0 10.6ZM12.8 12.8l3.7 3.7',
  }

  function openPR(n) {
    history.pushState({ pr: true }, '', prHash(roomId, n))
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
    if (drill) return false
    const issue = parseIssueHash(location.hash)
    const pr = parsePRHash(location.hash)
    const file = parseFileHash(location.hash)
    const link = issue ? ['issues', issue] : pr ? ['github', pr] : file ? ['files', file] : null
    return !!link && link[1].roomId === roomId && link[0] === view
  }

  // syncRoute puts the open screen's address in the address bar
  // (lib/ui/route.ts). A followed link passes through other screens while it
  // loads, so routing holds the address until it lands. The first write, and
  // the one after a followed link, correct the current entry instead of adding one.
  function syncRoute() {
    // A service's read-only conversation keeps the Services address.
    const session = readOnlyDm ? '' : routeSession(drill)
    const hash = routeHash({ room: roomId, view, session, turn: session ? sessionTurn : 0, run: view === 'runs' ? openRun : '' })
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
  function routeHere(r) {
    // A followed address always ends a detail navigation that is still loading:
    // its older response must not open over the screen the reader asked for.
    detailRequest++
    if (r.session ? routeSession(drill) === r.session && sessionTurn === r.turn : r.run ? !drill && view === 'runs' && openRun === r.run : !drill && !openRun && view === r.view) return false
    if (!r.session && !r.run) openView(r.view)
    return { roomId, request: detailRequest }
  }

  function openView(v) {
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
    if (v !== 'goals') goalId = ''
    navOpen = false
    channelMenu = null
    lookOpen = false
    if (v === 'runs') loadRuns()
  }

  // Completion links open stored UI details. They never invoke a command.
  async function openDetail(target) {
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
        if (request === detailRequest) say(String(e?.message || e))
      }
    }
  }

  function openSettings(start = 'profiles', pairNow = false, profile = '', accountId = '') {
    navOpen = false
    settingsAt = { start, pairNow, profile, accountId }
    settingsKey++
    showSettings = true
  }

  function createProfile(selection) {
    settingsAt = { start: 'profiles', pairNow: false, initialSelection: selection }
    settingsKey++
    showSettings = true
  }

  // Slash argument parsing stays here; menu and palette actions call the
  // same handlers with no argument.
  let templates = $state(null)
  function loadTemplates() {
    if (!templates) api.listTemplates().then((t) => (templates = t ?? []), () => (templates = []))
    return templates ?? []
  }
  function actionRegistry(r = current ?? room) {
    return actions({
      room: r, rooms, icons: ICON, hasTerminal, narrow, runs, session: drill, sessionPRs: prMap[drill?.id],
      openView, openSettings, renameRoom, deleteRoom, roomAction, say, loadRooms,
      openFiles: (path) => { openView('files'); if (path) reveal = path.replace(/^\.?\//, '') },
      openPins: () => { showPins = true; if (narrow) showMembers = false },
      openFind: (arg) => openFind(arg || ''),
      openRoomSettings: async (target) => { if (target.id === roomId || await selectRoom(target.id)) showRoomSettings = true },
      openSessionPRs: () => (sessionTab = 'prs'),
      cancelRun: (run) => (confirm = { kind: 'cancelrun', run }),
      toggleSidebar: () => (narrow ? (navOpen = !navOpen) : (showSide = !showSide)),
      roomOptions: () => rooms.filter((x) => x.id !== r.id).map((x) => ({ value: x.name, hint: x.displayName && x.displayName !== x.name ? x.displayName : x.repo || '' })),
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
        const name = arg.split(/\s+/)[0]
        const text = arg.slice(name.length).trim()
        let input = null
        if (text) {
          try { input = JSON.parse(text) } catch { input = text }
        }
        const res = await api.startRun(r.id, { template: name }, input)
        say(`${name} started.`)
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
  let targets = $derived(showPalette && roomId ? paletteTargets(hierarchy, runs?.runs ?? [], changes ?? []) : [])
  $effect(() => { if (showPalette && roomId && runs === null) untrack(loadRuns) })
  function openPaletteTarget(t) {
    if (t.kind === 'session') openSession(t.session)
    else if (t.kind === 'run') showRun(t.run)
    else openPR(t.pr)
  }

  // A room chosen in the palette. A room just made is not in the list yet.
  async function paletteRoom(id) {
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
    api.dataDir().then((d) => (dataDir = d))
    api.health().then((h) => (health = h?.status ?? ''))
    loadProfiles()
    // Check, never install: `dyna update` stays an explicit step.
    api.updateNotice().then((n) => (update = n?.text ? n : null)).catch(() => {})
    openFirst()
    const link = watch((r) => (route = r))
    // The call widget lives outside every view; it takes the same events.
    const engine = engineWatch((s) => (engineStatus = s), browserEngine(engineCheck))
    const off = onEvent((ev) => { handle(ev); voiceWidget?.event(ev) }, (up) => { link.streamState(up); engine.state(up) })
    registerWorker()
    const fromWorker = (e) => {
      if (e.data?.type !== 'open') return
      callPush = callLink(e.data.room, e.data.call ?? null) ?? callPush
      openTarget(e.data.room, e.data.post)
    }
    navigator.serviceWorker?.addEventListener('message', fromWorker)
    window.addEventListener('hashchange', hashChanged)
    const clicks = (e) => {
      const a = e.target.closest?.('a[href]')
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
    <RoomRail {rooms} working={workingAgents} selected={agentsNav ? '' : roomId} onselect={selectRoom} onmenu={(r, x, y, anchor) => (roomMenu = { r, x, y, anchor })} />
    {#if showSide || narrow}
      <Sidebar agents={agentsNav ? agents : null} selected={drill?.address ?? ''} onroom={(id) => selectRoom(id)} {computers} {channels} channelUnread={feed?.channelUnread ?? {}} {showClosed} room={current ?? room} prs={openPRMap} onroommenu={(x, y, anchor) => (roomMenu = { r: current ?? room, x, y, anchor })} {view} questions={questions.filter((q) => q.state === 'open' && q.to === 'owner').length} {live} {runs} {dataDir} {ownerHue} {route} update={!!update} onsettings={() => openSettings()} onlogout={signOut} onview={openView} onmember={openAgent} />
    {/if}
  </div>
  {#if narrow && navOpen}
    <button class="scrim" aria-label="Close the room list" onclick={() => (navOpen = false)}></button>
  {/if}
  <main class="main">
    <header class="bar" class:dm-bar={!!drill}>
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
        <span class="title">Dyna</span>
        {@const head = agentsHeader(agents)}
        <span class="meta">{head.rooms}{#if head.rooms && head.working}&nbsp;·&nbsp;{/if}{#if head.working}<em>{head.working} working</em>{/if}</span>
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
          {#if !narrow}<div class="dm-tabs"><SessionTabs bind:tab={sessionTab} media={session?.media ?? []} prs={prMap[drill.id] ?? []} computer={computers && !drill.rootSeat ? computerOf(drill.id) ?? { state: 'none' } : null} changes={sessionChanged} shells={drillShells.length} /></div>{/if}
          <span class="spacer"></span>
          <UseIcons shells={drillShells.length} computer={inUse(drillComputer)} tab={sessionTab} onselect={(tab) => (sessionTab = tab)} />
          {#if isRootDm}<CallButton {room} />{/if}
          <button class="dm-threads" class:on={contextOpen} aria-expanded={contextOpen} title="Pull requests and shell jobs" onclick={() => { contextOpen = !contextOpen; if (contextOpen) sessionTab = 'chat' }}>Context</button>
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
          {#if workTabs && !narrow}<div class="dm-tabs"><Tabs tabs={workTabs} tab={view} label={workRow.label} onselect={openView} /></div>{/if}
        {:else}
          <div class="trail">
            <button class="crumb" class:current={!channelTrail.length} title={trailTitle} onclick={() => openView(GENERAL)}><span class="hash">#</span>general</button>
            {#each channelTrail as ancestor, i (ancestor.id)}
              <span class="slash" aria-hidden="true">/</span>
              <button class="crumb" class:current={i === channelTrail.length - 1} title={trailTitle} onclick={() => openView(channelView(ancestor.id))}>{ancestor.name}</button>
            {/each}
          </div>
          <span class="topic mono" title={room.repo}>{room.repo || ''}</span>
        {/if}
        {#if !drill}
        <span class="spacer"></span>
        <CallButton {room} />
        {#if isChannel(view) && !workRow && !narrow}<LookMenu scope={scopeOf(roomId, view)} />{/if}
        <span class="meta">{Math.max(0, people.length - 1)} session{people.length === 2 ? '' : 's'}{#if live.length}&nbsp;·&nbsp;<em>{live.length} working</em>{/if}</span>
        {#if syncRemotes.length}<SyncBadge remotes={syncRemotes} status={syncRepo} note={syncNote} onrefresh={() => syncAction(api.syncRefresh)} onexport={() => syncAction(api.gitExport)} />{/if}
        {#if feedShown}
          <button class="icon find-btn" class:on={finding} onclick={() => (finding ? (finding = false) : openFind())} title={finding ? 'Close find' : 'Find in this room (Ctrl+F)'} aria-label={finding ? 'Close find' : 'Find in this room'} aria-expanded={finding}><SearchIcon /></button>
        {/if}
        {#if narrow}
        <!-- A phone keeps the bar for the page title, so the panel toggles and
             the channel look go into one menu. -->
        <button class="icon" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); channelMenu = { x: r.right - 240, y: r.bottom + 6 } }} title="Channel actions" aria-label="Channel actions" aria-haspopup="menu" aria-expanded={!!channelMenu}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><circle cx="5" cy="12" r="1.7" fill="currentColor" /><circle cx="12" cy="12" r="1.7" fill="currentColor" /><circle cx="19" cy="12" r="1.7" fill="currentColor" /></svg>
        </button>
        {:else}
        <button class="icon pin" class:on={showPins} onclick={() => { showPins = !showPins; if (showPins) showIssues = false }} title={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-label={showPins ? 'Hide pinned posts' : 'Show pinned posts'} aria-expanded={showPins}>
          <PinIcon size={21} />
          {#if pins.length}<span class="badge">{pins.length}</span>{/if}
        </button>
        {#if issues.length || showIssues}
          <button class="icon pin" class:on={showIssues} onclick={() => { showIssues = !showIssues; if (showIssues) showPins = false }} title={showIssues ? 'Hide issues' : 'Show issues'} aria-label={showIssues ? 'Hide issues' : 'Show issues'} aria-expanded={showIssues}>
            <svg viewBox="0 0 24 24" width="21" height="21" aria-hidden="true"><circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M12 7.5v5.5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><circle cx="12" cy="16.2" r="1" fill="currentColor" /></svg>
            <span class="badge">{issues.length}</span>
          </button>
        {/if}
        <button class="icon" class:on={showMembers} onclick={() => { showMembers = !showMembers; if (showMembers) showIssues = false }} title={showMembers ? 'Hide member list' : 'Show member list'} aria-label={showMembers ? 'Hide member list' : 'Show member list'} aria-expanded={showMembers}>
          <svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><circle cx="9" cy="8" r="3.2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><circle cx="16.5" cy="9" r="2.4" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M16 13.6c2.4 0 4 1.5 4.5 4.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
        </button>
        {/if}
        {/if}
      {/if}
      {#if !roomId}<span class="spacer"></span>{/if}
      {#if narrow}
        <button class="icon" onclick={() => (showPalette = true)} title="Search rooms and commands" aria-label="Search rooms and commands"><SearchIcon size={22} /></button>
      {/if}
    </header>
    {#if finding && feedShown}
      <FeedFind bind:this={feedFind} roomId={room.id} roomName={room.name} initial={findText} onjump={jumpTo} onclose={() => (finding = false)} />
    {/if}
    <div class="content">
      {#key viewIdentity({ roomId, view, drill })}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="center" in:fade={{ duration: prView ? 0 : motion(FAST) }} ondragover={dragOver} ondragleave={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) dropping = false }} ondrop={drop}>
        {#if dropping}
          <div class="dropzone"><div><strong>Drop to attach</strong><span>to your next post in {drill ? '@' + drill.name : '#' + roomLabel(current ?? room)}</span></div></div>
        {/if}
        {#if engineStatus}
          <p class="engine-down" class:waiting={engineStatus === 'reconnecting'} role={engineStatus === 'reconnecting' ? 'status' : 'alert'}><strong>Reconnecting…</strong>
            {#if engineStatus === 'offline'}This device is offline. Dyna reconnects when its network is back.
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
        {#if !roomId}
          <Home bind:this={home} {rooms} {update} onroom={selectRoom} onaccount={(id) => openSettings('usage', false, '', id)} onagent={openAgent} />
        {:else if !room}
          <Skeleton label="Loading room" />
        {:else if drill}
          <!-- Keyed: staged files and the typed draft belong to one session. -->
          {#if narrow && !readOnlyDm}<SessionTabs bind:tab={sessionTab} media={session?.media ?? []} prs={prMap[drill.id] ?? []} computer={computers && !drill.rootSeat ? computerOf(drill.id) ?? { state: 'none' } : null} changes={sessionChanged} shells={drillShells.length} />{/if}
          {#key drill.id}
            {#if sessionTab === 'computer' && computers}
              <ComputerPane c={computerOf(drill.id) ?? { session: drill.id, name: drill.name, state: 'none', apps: [], stream: {}, viewers: 0, rssKiB: 0, streamRssKiB: 0 }} room={room.id} feed={computerFeed[drill.id] ?? []} chat={{ computerCount: 1, draft: drafts['computer:' + roomId + ':' + drill.id] ?? '', ondraft: computerDraft, session, room, members: hierarchy, onsend: sendToSession, onopenfile: (a) => (viewing = a), onpost: jumpTo, onmessages: () => (sessionTab = 'chat') }} />
            {:else if sessionTab === 'shell'}
              <ShellGrid jobs={drillShells} onjob={(job) => { openJob = job.id; drill = null; view = 'jobs' }} />
            {:else if sessionTab === 'media'}
              <SessionMedia onpost={jumpTo} {room} posts={session?.media ?? []} onopenfile={(a) => (viewing = a)} />
            {:else if sessionTab === 'changes'}
              <Changes roomId={room.id} session={drill.id} sessionName={drill.name} />
            {:else if sessionTab === 'prs'}
              <PRPanel prs={prMap[drill.id] ?? []} error={prError} names={Object.fromEntries(members.filter((x) => x.id).map((x) => [x.id, x.name]))} onuntrack={untrackPR} onresume={resumePR} />
            {:else}
              <div class="sess-wrap" style:--shift={threadsOpen && !narrow ? '360px' : '0px'}>
              <Session readOnly={readOnlyDm} bind:readingHistory={sessionReadingHistory} onissue={openIssues} onreopen={reopenSession} {reopening} ondismisssession={dismissSession} {dismissing} onpost={jumpTo} bind:this={sessionView} bind:threadsOpen bind:contextOpen bind:selectedThread bind:draft={sessionDraft.value} members={hierarchy} prs={prMap[drill.id] ?? []} jobs={jobs ?? []} contextError={jobsError} onchild={openSession} onpr={() => (sessionTab = 'prs')} onjob={(job) => { openJob = job.id; drill = null; view = 'jobs' }} onopenfile={(a) => (viewing = a)} {session} error={sessionError} {room} {queue} focusedTurn={sessionTurn} onrecent={() => openSession(drill)} onearlier={loadEarlierSession} onnewer={loadNewerSession} onpresent={presentSession} onunqueue={unqueue} ondismiss={dismissPendingPost} onharness={(name) => openSettings('profiles', false, name)} onsend={sendToSession} />
              {#if showPreview}<ComputerPreview c={drillComputer} session={drill.id} name={drill.name} onopen={() => (sessionTab = 'computer')} />{/if}
              </div>
            {/if}
          {/key}
        {:else}
        {#if workTabs && narrow}<Tabs tabs={workTabs} tab={view} label={workRow.label} onselect={openView} />{/if}
        {#if view === 'files'}
          {#key room.id}<FileBrowser roomId={room.id} {narrow} {reveal} folder={revealFolder} onpr={openPR} />{/key}
        {:else if view === 'cihealth'}
          {#key room.id}<CIHealth roomId={room.id} {narrow} {changes} onpr={openPR} />{/key}
        {:else if view === 'library'}
          {#key room.id}<Library roomId={room.id} {narrow} onopen={({ path, dir }) => { openView('files'); if (dir) revealFolder = { path }; else reveal = path }} />{/key}
        {:else if view === 'github' || view === 'issues'}
          {#key `${room.id}:${view}`}<GitHub older={listPages.changes?.older} loadingOlder={olderLoading} failedOlder={olderFailed} onolder={() => loadOlderLists(['changes'])} roomId={room.id} kind={view === 'issues' ? 'issue' : 'pr'} integration={syncRepo?.integration ?? null} link={prLink} {changes} members={hierarchy} onask={askAgent} onsession={(m, tab) => { openSession(m); if (tab) sessionTab = tab }} onpr={openPR} ontrack={trackPR} onuntrack={untrackPR} onresume={resumePR} {reviewer} onreviewer={loadReviewer} goals={knownGoals} onexpedite={expediteGoal} onlinkgoal={linkPRGoal} />{/key}
        {:else if view === 'terminal' && hasTerminal}
          {#key room.id}<Terminal roomId={room.id} ref={room.id} noRepo={!room.repo} {narrow} names={Object.fromEntries(members.filter((m) => m.id).map((m) => [m.id, m.name]))} />{/key}
        {:else if view === 'questions'}
          {#key room.id}<Questions roomId={room.id} {questions} focusId={questionFocus} focusRequest={questionFocusRequest} onanswer={answerQuestion} onopenfile={(a) => (viewing = a)} />{/key}
        {:else if view === 'services'}
          {#key room.id}<Services roomId={room.id} members={hierarchy} {changes} onopen={(m) => openSession(m, 0, null, true)} />{/key}
        {:else if view === 'visuals'}
          {#key room.id}<Visuals roomId={room.id} {visuals} onopen={(v) => (viewing = { ...v.attachment, postId: v.post.id })} />{/key}
        {:else if view === 'computers' && computers}
          {#key room.id}<Computers list={computers} error={computerError} onopen={(c) => { const m = hierarchy.find((x) => x.id === c.session); if (m) { openSession(m); sessionTab = 'computer' } else popOut(room.id, c.session) }} />{/key}
        {:else if view === 'jobs'}
          {#key room.id}<Jobs older={listPages.jobs?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['jobs'])} {jobs} error={jobsError} jobId={openJob} {narrow} onstop={stopJob} />{/key}
        {:else if view === 'goals'}
          {#key room.id}<Goals roomId={room.id} older={listPages.goals?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['goals'])} {goals} members={hierarchy} selected={goalId} onselect={(id) => (goalId = id)} ongoal={openGoal} onexpedite={(g) => expediteGoal(g).catch((e) => say(String(e?.message || e)))} />{/key}
        {:else if view === 'runs'}
          {#if openRun}
            {#key openRun}<Run detail={runDetail} error={runError} oncancel={(run) => (confirm = { kind: 'cancelrun', run })} onpause={pauseRun} onsteer={steerAgent} />{/key}
          {:else}
            <Runs {room} list={runs} error={runsError} onopen={showRun} onstart={() => (starting = true)} />
          {/if}
        {:else}
          <Feed {goals} bind:this={feedView} bind:stuck={feedAtBottom} label={channelLabel} {room} posts={shownPosts} onreveal={revealPost} {questions} calls={view === GENERAL ? roomCalls : []} onacceptcall={acceptCall} ondeclinecall={declineCall} {view} earlier={feed?.earlier ?? 0} onearlier={(near) => loadEarlier('older', '', near)} newer={feed?.newer ?? 0} onnewer={(near) => loadEarlier('newer', '', near)} onpresent={() => loadEarlier('present')} onaround={(id) => loadEarlier('around', id)} {unreadFrom} pinned={pinnedIds} onaddress={openAddress} onreply={startReply} onforward={(p) => composer?.forward(p)} onpin={togglePin} ondetail={openDetail} onquestion={openQuestion} ongoal={openGoal} onissue={openIssues} onopenfile={(a) => (viewing = a)} />
          <!-- Keyed: staged files belong to one room and never follow a switch. -->
          {#key room.id + view}
            <Composer onopenfile={(a) => (viewing = a)} bind:this={composer} label={channelLabel} prepareText={(text) => prepareChannelText(text, channel, channels, (value) => api.preview(room.id, value))} {room} {choices} {profiles} commands={composerCommands} onslash={() => { loadProfiles(); loadTemplates() }} {reply} bind:draft={draft.value} onsend={send} oncancelreply={() => (reply = null)} />
          {/key}
        {/if}
        {/if}
      </div>
      {/key}
      {#if narrow && roomId && room && (showPins || showIssues || (showMembers && !drill) || (view === 'goals' && selectedGoal))}
        <button class="scrim" aria-label="Close the panel" onclick={() => { showPins = false; showIssues = false; showMembers = false; goalId = '' }}></button>
      {/if}
      <!-- On a wide screen this wrapper adds no box; on a narrow one it is a sheet. -->
      <div class="panel">
      {#if roomId && room && view === 'goals' && selectedGoal}
        {#key selectedGoal.id}<GoalDetail roomId={room.id} goal={selectedGoal} members={hierarchy} onchanged={loadGoals} onclose={() => (goalId = '')} onexpedite={expediteGoal} />{/key}
      {:else if roomId && room && showPins}
        <Pins {room} {pins} ondetail={openDetail} onjump={(id) => { if (narrow) showPins = false; jumpTo(id) }} onunpin={togglePin} onopenfile={(a) => (viewing = a)} onclose={() => (showPins = false)} />
      {:else if roomId && room && showIssues}
        <Issues open={issues} history={issueHistory} onhistory={() => loadIssues(true)} onclose={() => (showIssues = false)}
          ongoal={(i) => triageIssue(() => api.issueGoal(roomId, i.id))} onexport={(i) => triageIssue(() => api.exportIssue(roomId, i.id))} oncloseissue={askCloseIssue} />
      {:else if roomId && room && showMembers && !drill}
        <Members computers={computers ?? []} onopencomputer={(m) => { openSession(m); sessionTab = 'computer' }} members={people} focus={view.startsWith('channel:') ? view.slice('channel:'.length) : ''} scope="{room.id}|{view}" {roomControls} controlMembers={hierarchy} onroomactions={roomSessionActions} onresumeroom={() => roomSessionController?.run('resume')} onreloadcontrols={() => roomSessionController?.load()} oncleardormant={askClearDormant} ondismiss={(m) => dismissSession(m).catch((e) => say(`Could not dismiss ${m.name}: ${String(e?.message || e)}`))} {dismissing} ontakeseat={takeBackSeat} {fresh} selecting={selectingSessions} picked={pickedSessions} onselection={toggleSessionSelection} onpick={pickSession} onactions={selectionActions} selected={drill?.address ?? ''} closed={showClosed} onselect={openMember} onmenu={(m, x, y, anchor) => (menu = { m, x, y, anchor })} onclosed={() => { showClosed = !showClosed; loadMembers() }} />
      {/if}
      </div>
    </div>
  </main>
  <VoiceWidget bind:this={voiceWidget} bind:link={callPush} {narrow} {rooms} onopenfile={(a) => (viewing = a)} />
</div>
{/if}
{#if showPalette}
  <Palette {rooms} commands={paletteCommands} {targets} path={palettePath} onroom={paletteRoom} ontarget={openPaletteTarget} onclose={() => { showPalette = false; palettePath = null }} />
{/if}
{#if viewing && room}
  <Viewer roomId={viewing.roomId ?? room.id} att={viewing} onjump={viewing.postId && (viewing.roomId ?? room.id) === room.id ? () => { const id = viewing.postId; viewing = null; jumpTo(id) } : null} onclose={() => (viewing = null)} />
{/if}
{#if showSettings}
  {#key settingsKey}
    <Settings {update} {dataDir} start={settingsAt.start} pairNow={settingsAt.pairNow} profile={settingsAt.profile ?? ''} accountId={settingsAt.accountId ?? ''} initialSelection={settingsAt.initialSelection} roomId={room?.id ?? ''} oncreateprofile={createProfile} onclose={() => { showSettings = false; loadProfiles(); home?.refresh(); voiceCall.refresh() }} />
  {/key}
{/if}
{#if showRoomSettings && room}
  {#snippet roomRules()}<Rules roomId={room.id} view={rules} members={hierarchy} {ownerHue} bind:editing={rulesEditing} onchanged={loadRules} />{/snippet}
  <RoomSettings {room} {reviewer} onreviewer={loadReviewer} controls={roomControls} onsessions={(action) => roomSessionController?.run(action)} onaction={(id) => actionRegistry(room).find((a) => a.id === id)?.run()} rules={roomRules} busy={rulesEditing} onclose={() => (showRoomSettings = false)} />
{/if}
{#if roomMenu}
  <Menu x={roomMenu.x} y={roomMenu.y} title={roomLabel(roomMenu.r)} items={roomMenuItems(roomMenu.r)} onclose={() => { const a = roomMenu?.anchor; roomMenu = null; a?.focus?.() }} />
{/if}
{#if channelMenu}
  <Menu x={channelMenu.x} y={channelMenu.y} title="Channel" items={channelMenuItems()} onclose={() => (channelMenu = null)} />
{/if}
{#if narrow && room && !drill && isChannel(view) && !workRow}
  <LookMenu scope={scopeOf(roomId, view)} anchored={false} bind:open={lookOpen} />
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
{#if currentBulk && currentBulk.roomId === roomId}
  {@const batch = { ...currentBulk }}
  {#key batch.key}<BulkSessionAction {batch} members={hierarchy} onclose={() => (activeBulk = 0)} onrun={(force) => runBulk(batch.key, force)} onconfigure={(patch) => bulkControllers.get(batch.key)?.configure(patch)} onreload={() => bulkControllers.get(batch.key)?.load()} onselectfailed={() => selectBulkFailures(batch)} />{/key}
{/if}
{#if consolidating && room && consolidating.roomId === roomId}
  {@const context = { ...consolidating }}
  {#key context.key}<SessionConsolidation roomId={context.roomId} sessions={context.sessions} onoperation={(operation) => consolidationChanged(context, operation)} onclose={closeConsolidation} onopen={(id) => { const m = hierarchy.find((m) => m.id === id); closeConsolidation(); if (m) openSession(m) }} />{/key}
{/if}
{#if transferring && room}
  <ModelTransfer member={transferring} roomId={room.id} {profiles} onclose={() => { transferring = null; loadMembers(); if (drill) loadSession() }} />
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
  <section class="toast close-result" role="status" aria-label="Session close result">
    <strong>{closeResult.name} is closed.</strong>
    {#if closeResult.items.length}<ul>{#each closeResult.items as item}<li>{item}</li>{/each}</ul>{/if}
    <button onclick={() => (closeResult = null)}>Dismiss</button>
  </section>
{:else if toast}
  <div class="toast" role="status">{toast}</div>
{/if}
{#if card && room}
  <ProfileModal {goals} older={listPages.goals?.older} loadingOlder={olderLoading} onolder={() => loadOlderLists(['goals'])} ongoal={(id) => { card = null; openGoal(id) }} onpost={jumpTo} onopenfile={(a) => (viewing = a)} {profile} error={profileError} {room} ondetail={openDetail} onclose={() => (card = null)} onmessage={openSession} onmention={mentionMember} />
{/if}

<style>
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
  .topic {
    color: var(--faint);
    font-size: 12.5px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding-left: 12px;
    margin-left: 4px;
    border-left: 1px solid var(--line);
  }
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
  .close-result { max-width: min(640px, calc(100vw - 32px)); max-height: 50vh; overflow: auto; overflow-wrap: anywhere; }
  .close-result ul { margin: 8px 0; padding-left: 20px; }
  .close-result button { margin-top: 8px; }

  .toast {
    position: fixed;
    left: 50%;
    bottom: 84px;
    transform: translateX(-50%);
    z-index: 70;
    background: #121113;
    border: 1px solid var(--line-soft);
    color: var(--ink-2);
    font-size: 13px;
    padding: 8px 14px;
    border-radius: 8px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
  }

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
    .topic, .meta, .slash, .chip { display: none; }
    /* A phone finds through the palette or /find; the bar has no room for a second search icon. */
    .find-btn { display: none; }
    .dm-bar .back-name, .dm-bar .back-arrow, .dm-bar .dm-sep { display: none; }
    .dm-bar > .spacer { display: none; }
    .dm-bar .dm-back { width: 36px; max-width: none; font-size: 0; padding: 6px; }
    .dm-bar .dm-back::before { content: '←'; font-size: 18px; }
    .dm-threads, .dm-back { min-height: 44px; }
    .dm-bar > .icon:last-child { display: none; }
    /* The page title needs a readable width: on a phone it is the only thing
       that says which page is open, so it never shrinks past 96 px and the
       bar's buttons shrink around it. A nested channel shows its current crumb
       only: the whole trail stays in that crumb's tooltip. */
    .title, .crumb { min-width: 96px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .trail { gap: 4px; }
    .trail .crumb:not(.current), .trail .slash { display: none; }
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
    .toast { bottom: calc(96px + env(safe-area-inset-bottom)); max-width: calc(100vw - 32px); }
  }
</style>
