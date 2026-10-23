// The member selection, its bulk actions and the consolidation screen: which
// sessions the reader picked, the batch each bulk action reports through, and
// the screen that merges two or more of them. lib/sessions holds the request
// helpers and the two panels, and lib/app/sessionView.svelte.ts holds the open
// transcript; this module keeps the state and the order, so App.svelte only
// hosts them.
import { api } from '../../api.ts'
import { bulkRetryable, createBulkSessionAction } from '../sessions/bulkSessions.ts'
import { pruneSelection, selectableSession, selectionKey, toggleSelection } from '../sessions/consolidation.ts'
import { router } from './router.svelte.ts'
import type { Member } from '../api/room.ts'
import type { BulkAction, BulkValue } from '../sessions/bulkSessions.ts'

/** One row of the selection menu, as lib/ui/Menu.svelte draws it. */
interface SelectionItem {
  label: string
  run: () => unknown
  hint?: string
  danger?: boolean
  gap?: boolean
  disabled?: boolean
}

/** The selection a menu or a bulk panel was opened for. A newer pick replaces it. */
export interface SelectionContext {
  roomId: string
  ids: string[]
  key: string
}

/** One bulk batch, kept so its panel can show and retry its results. */
export interface BulkRecord {
  key: number
  roomId: string
  roomRequest: number
  selectionRevision: number
  sessions: (Member & { id: string })[]
  action: BulkAction
  value?: BulkValue
}

/** The sessions the consolidation screen will merge. */
interface Consolidation {
  roomId: string
  sessions: Member[]
  key: number
}

/** The page's own side of the selection. App binds these once. */
export interface SelectionHost {
  /** The room data's visit, so a batch for an older visit is dropped. */
  visit(): number
  /** The page still shows the room the reader chose. */
  alive(): boolean
  /** The room's sessions, as the member list draws them. */
  hierarchy(): Member[]
  /** The member rows without reviewers, which Select all reads. */
  people(): Member[]
  /** Mark a new detail request, so a slow one stops. */
  newDetail(): void
  /** Write the picked addresses into the room's draft, and return its request. */
  message(members: Member[]): number
  /** Focus the composer after the page draws, if nothing newer opened. */
  focusMessage(request: number, alive: () => boolean): void
  /** The session menu closes as the consolidation screen opens. */
  closeSessionMenu(): void
  /** Show the member list, which a consolidation pick needs. */
  showMembers(): void
  loadMembers(): void
  loadChanges(): void
  /** Read the open transcript again, if one is open. */
  loadSession(): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`selection: ${name} is unbound`) }
let selectionHost: SelectionHost = {
  visit: () => missing('visit'),
  alive: () => missing('alive'),
  hierarchy: () => missing('hierarchy'),
  people: () => missing('people'),
  newDetail: () => missing('newDetail'),
  message: () => missing('message'),
  focusMessage: () => missing('focusMessage'),
  closeSessionMenu: () => missing('closeSessionMenu'),
  showMembers: () => missing('showMembers'),
  loadMembers: () => missing('loadMembers'),
  loadChanges: () => missing('loadChanges'),
  loadSession: () => missing('loadSession'),
}

export function bindSelection(bound: SelectionHost): void {
  selectionHost = bound
}

const roomId = $derived(router.roomId)

let selectingSessions = $state(false)
let pickedSessions = $state<string[]>([])
let selectionRevision = 0
let selectionMenu = $state<(SelectionContext & { x: number; y: number; anchor?: HTMLElement }) | null>(null)
let activeBulk = $state(0)
let bulkBatches = $state<BulkRecord[]>([])
let bulkSerial = 0
const bulkControllers = new Map<number, ReturnType<typeof createBulkSessionAction>>()
let currentBulk = $derived(bulkBatches.find((b) => b.key === activeBulk))
let consolidating = $state<Consolidation | null>(null)
let consolidationView = 0

function setPicked(ids: string[]) {
  if (JSON.stringify(ids) !== JSON.stringify(pickedSessions)) { pickedSessions = ids; selectionRevision++ }
}

// applyRows drops the picked sessions a new member list no longer holds.
function applyRows(rows: Member[]) {
  setPicked(pruneSelection(pickedSessions, rows))
}

function bulkChanged(record: BulkRecord, value: BulkValue) {
  if (!selectionHost.alive()) return
  const old = bulkBatches.find((b) => b.key === record.key)?.value
  const next = { ...record, value }
  bulkBatches = bulkBatches.some((b) => b.key === record.key) ? bulkBatches.map((b) => b.key === record.key ? next : b) : [next, ...bulkBatches]
  if (record.roomId === roomId && record.roomRequest === selectionHost.visit() && record.selectionRevision === selectionRevision && value.started) {
    const byId = new Map(value.results.map((r) => [r.session, r]))
    setPicked(record.sessions.map((m) => m.id).filter((id) => { const result = byId.get(id); return !result || bulkRetryable(result, record.action) }))
    record.selectionRevision = selectionRevision
  }
  if (old?.busy && !value.busy && value.started && record.roomId === roomId && record.roomRequest === selectionHost.visit()) { selectionHost.loadMembers(); selectionHost.loadChanges() }
}

function openBulkAction(action: BulkAction, context: SelectionContext) {
  if (!currentSelection(context)) return
  const sessions = context.ids.map((id) => selectionHost.hierarchy().find((m) => m.id === id)).filter((m): m is Member & { id: string } => !!m?.id && selectableSession(m)).map((m) => ({ ...m }))
  if (!sessions.length) return
  selectionHost.newDetail()
  const record = { key: ++bulkSerial, roomId, roomRequest: selectionHost.visit(), selectionRevision, sessions, action }
  const controller = createBulkSessionAction(api, record, (value) => bulkChanged(record, value))
  bulkControllers.set(record.key, controller)
  activeBulk = record.key
  controller.load()
}

function runBulk(key: number, force = false) { bulkControllers.get(key)?.run(force) }
function configureBulk(key: number, patch: Partial<Pick<BulkValue, 'profile' | 'parent'>>) { bulkControllers.get(key)?.configure(patch) }
function loadBulk(key: number) { bulkControllers.get(key)?.load() }
// refreshTransfers asks each batch for a running transfer's next phase.
function refreshTransfers() {
  for (const controller of bulkControllers.values()) controller.refreshTransfers()
}
// closeBulk closes the batch panel but keeps its record.
function closeBulk() { activeBulk = 0 }

function selectBulkFailures(batch: BulkRecord) {
  if (batch.roomId !== roomId) return
  const failed = (batch.value?.results ?? []).filter((r) => bulkRetryable(r, batch.action)).map((r) => r.session)
  setPicked(pruneSelection(failed, selectionHost.hierarchy())); selectingSessions = true
  activeBulk = 0
}

function selectionActions(x: number, y: number, anchor?: HTMLElement) {
  selectionMenu = { x, y, anchor, roomId, ids: [...pickedSessions], key: selectionKey(roomId, pickedSessions) }
}

function closeSelectionMenu() { const anchor = selectionMenu?.anchor; selectionMenu = null; anchor?.focus?.() }

function currentSelection(context: SelectionContext | null | undefined) { return context?.roomId === roomId && context.key === selectionKey(roomId, pickedSessions) }

function selectionItems(context: SelectionContext): SelectionItem[] {
  return [
    { label: 'Select all sessions', run: () => { if (currentSelection(context)) setPicked(selectionHost.people().filter(selectableSession).flatMap((m) => m.id ? [m.id] : [])) } },
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

// selectForConsolidation starts a selection from one session's menu, so the
// reader can pick the others in the member list.
function selectForConsolidation(m: Member) {
  selectingSessions = true
  setPicked(m.id ? [m.id] : [])
  selectionHost.showMembers()
}

function messageSelection(context: SelectionContext) {
  if (!currentSelection(context)) return
  const selected = context.ids.map((id) => selectionHost.hierarchy().find((m) => m.id === id)).filter((m): m is Member => !!m && selectableSession(m))
  const focusRequest = selectionHost.message(selected)
  selectingSessions = false; setPicked([])
  selectionHost.focusMessage(focusRequest, () => selectionHost.alive() && context.roomId === roomId)
}

function toggleSessionSelection() {
  selectingSessions = !selectingSessions
  if (!selectingSessions) { setPicked([]); selectionRevision++ }
}

function pickSession(m: Member, checked: boolean, begin = false) { if (!m.id) return; if (begin) selectingSessions = true; setPicked(toggleSelection(pickedSessions, { ...m, id: m.id }, checked)) }

function openConsolidation() {
  const ids = pruneSelection(pickedSessions, selectionHost.hierarchy())
  if (ids.length < 2 || ids.length > 16) return
  selectionHost.newDetail()
  const sessions = ids.flatMap((id) => { const member = selectionHost.hierarchy().find((m) => m.id === id); return member ? [member] : [] })
  consolidating = { roomId, sessions, key: ++consolidationView }
  selectionHost.closeSessionMenu()
}

function consolidationChanged(context: { key: number; roomId: string }, operation: { roomId: string }) {
  if (!selectionHost.alive() || (!consolidating || context.key !== consolidating.key) || context.roomId !== roomId || operation.roomId !== roomId) return
  selectingSessions = false
  setPicked([])
}

function closeConsolidation() {
  consolidating = null
  consolidationView++
  selectionHost.loadMembers()
  selectionHost.loadSession()
}

// clear leaves the selection, its panels and its batch results, as a room pick
// does.
function clear() {
  selectionMenu = null
  selectingSessions = false
  setPicked([])
  selectionRevision++
  activeBulk = 0
  consolidating = null
  consolidationView++
}

// destroy drops the batch controllers, which the page no longer shows.
function destroy() {
  for (const controller of bulkControllers.values()) controller.destroy()
  bulkControllers.clear()
}

/** The member selection, named as the page named it when it lived there. */
export const selectionView = {
  get selectingSessions() { return selectingSessions },
  get pickedSessions() { return pickedSessions },
  get selectionMenu() { return selectionMenu },
  get currentBulk() { return currentBulk },
  get consolidating() { return consolidating },
  setPicked,
  applyRows,
  clear,
  destroy,
  closeBulk,
  configureBulk,
  loadBulk,
  refreshTransfers,
  runBulk,
  selectBulkFailures,
  selectionActions,
  closeSelectionMenu,
  selectionItems,
  moveUnder,
  selectForConsolidation,
  toggleSessionSelection,
  pickSession,
  closeConsolidation,
  consolidationChanged,
}
