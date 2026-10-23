// The room's goals: the list the Goals view and the badges read, the one goal a
// link fetched that no loaded page holds, the room's expedited goal, and the
// Room settings rules list. lib/rooms/expedite.ts decides what an expedite
// change does; this module keeps the state and the requests, so App.svelte only
// hosts them.
import { get } from 'svelte/store'
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { clipText } from '../ui/toast.ts'
import { expedite, expediteAction, heldGoals, isExpedited } from '../rooms/expedite.ts'
import { expediteMenuItem } from '../rooms/expediteMenu.ts'
import { nextRules } from '../rooms/rules.ts'
import { router } from './router.svelte.ts'
import type { Goal } from '../api/goal.ts'
import type { Member } from '../api/room.ts'
import type { ListChange } from '../api/event.ts'
import type { ToastInput } from '../ui/toast.ts'

interface RuleList {
  hash: string
}
export interface RuleSet {
  room: RuleList
  global: RuleList
  hash: string
}

/** A page of one list, with its continuation cursor. */
interface ListPage<T> {
  items: T[] | null
  older?: boolean
  cursor?: string
  limit?: number
  loadedOlder?: boolean
}

/** The page's own side of the goals. App binds these once. */
export interface GoalsHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** Show a loader failure. */
  error(message: string): void
  /** Follow a link to another view. */
  openView(view: string): void
  /** Open the goals side panel on this goal. */
  selectGoal(id: string): void
  /** The goal panel takes the member list's place. */
  hidePins(): void
  /** The goal a link opened, or ''. */
  goalId(): string
  /** A goal would replace the room's expedited goal, so ask first. */
  askReplace(goal: Goal): void
  /** A session holds several goals, so let the reader pick one. */
  pickGoal(member: Member, goals: Goal[]): void
  /** The room's member tree, for the goal an agent holds. */
  hierarchy(): Member[]
  /** A goals list page joins the list. */
  applyPage(page: ListPage<Goal>, append?: boolean): Goal[]
  /** Say one line about a finished action. */
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`goals: ${name} is unbound`) }
let goalHost: GoalsHost = {
  reload: () => missing('reload'),
  error: () => missing('error'),
  openView: () => missing('openView'),
  selectGoal: () => missing('selectGoal'),
  hidePins: () => missing('hidePins'),
  goalId: () => missing('goalId'),
  askReplace: () => missing('askReplace'),
  pickGoal: () => missing('pickGoal'),
  hierarchy: () => missing('hierarchy'),
  applyPage: () => missing('applyPage'),
  say: () => missing('say'),
}

export function bindGoals(bound: GoalsHost): void {
  goalHost = bound
}

const roomId = $derived(router.roomId)

let goals = $state<Goal[]>([])
let fetchedGoal = $state<Goal | null>(null)
let fetchedGoalVersion = 0
// Items carry the expedite scope's assignees. They load for each room's
// expedited goal, room:goal -> {at, items}, and load again when the goal
// changes, so a new assignee gets the badge without a reload.
let expediteGoalItems = $state<Record<string, { at: string; items: { state?: string; assignee?: string }[] }>>({})
let rules = $state<RuleSet | null>(null)
let rulesPending = $state<RuleSet | null>(null)
let rulesEditing = $state(false)

let knownGoals = $derived.by(() => {
  const fetched = fetchedGoal
  return fetched && !goals.some((g) => g.id === fetched.id) ? [...goals, fetched] : goals
})

// withItems attaches the loaded items to the expedited goal, so the scope
// can cover item assignees.
function withItems(room: string, list: Goal[]) {
  const g = list.find((x) => isExpedited(x))
  const items = g && expediteGoalItems[room + ':' + g.id]?.items
  return g && items ? list.map((x) => (x.id === g.id ? { ...x, items } : x)) : list
}

// loadItems reads the items of the room's expedited goal, once per goal
// version. A goal without items has none to read.
function loadItems(room: string, goal: Goal) {
  const key = room + ':' + goal.id
  const cached = expediteGoalItems[key]
  if (cached && cached.at === goal.updatedAt) return
  api.goalItems(room, goal.id).then((items: { state?: string; assignee?: string }[] | null) => {
    expediteGoalItems = { ...expediteGoalItems, [key]: { at: goal.updatedAt, items: items ?? [] } }
  }, () => {})
}

function loadGoals() {
  return goalHost.reload('goals', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    try {
      const list = await api.goals(id)
      if (current() && roomId === id) {
        goals = goalHost.applyPage(list)
        await refreshFetchedGoal(current)
      }
    } catch (e) {
      if (current() && roomId === id) goalHost.error(String(e))
    }
  })
}

function loadRules() {
  return goalHost.reload('rules', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    try {
      const [room, global] = await Promise.all([api.rules(id, false), api.rules(id, true)])
      if (!current() || roomId !== id) return
      const v = { room, global, hash: `${global.hash}:${room.hash}` }
      const next = nextRules(rules, v, rulesEditing)
      rules = next.shown
      rulesPending = next.pending
    } catch (e) {
      if (current() && roomId === id) goalHost.error(String(e))
    }
  })
}

// An inline rules edit keeps the list it started from; when the edit closes,
// the saved answer takes its place.
function applyPendingRules() {
  if (!rulesEditing && rulesPending) { rules = rulesPending; rulesPending = null }
}

// A goal link may name a goal that no loaded page holds, such as an old
// done goal: load it, then select it.
// It resolves to the goal, or null when it cannot load.
async function openGoal(id: string) {
  goalHost.openView('goals')
  goalHost.selectGoal(id)
  goalHost.hidePins()
  const have = goalById(id)
  if (!id || have) return have ?? null
  const rid = roomId
  try {
    const g = await api.goal(rid, id)
    if (rid !== roomId || !g) return null
    fetchedGoal = g
    return g
  } catch (e) {
    if (rid === roomId) goalHost.say({ tone: 'error', subject: 'Could not open the goal', text: errorText(e) })
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

// Expedite gives the goal's agents and PRs the first review and merge.
// A goal that would replace another asks first.
async function expediteGoal(g: Goal) {
  if (g.room && g.room !== roomId) return
  const action = expediteAction(get(expedite), g)
  if (action === 'replace') { goalHost.askReplace(g); return }
  await api.expediteGoal(roomId, action === 'stop' ? '' : g.id, false)
  goalHost.say(action === 'stop'
    ? { tone: 'success', subject: 'Stopped expediting the goal', text: clipText(g.text) }
    : { tone: 'success', subject: 'Expedited the goal', text: clipText(g.text), action: { label: 'Open', run: () => openGoal(g.id) } })
  loadGoals()
}

// One menu item for the goals an agent holds, or its nearest ancestor holds.
// A session with one goal expedites it at once; several goals open a picker.
// expeditedId is the page's own expedited goal, read where the menu is drawn,
// so a goal that expires while the menu is open changes the item.
function expediteItems(m: Member, expeditedId: string) {
  if (m.closed) return []
  const held = heldGoals(m, goalHost.hierarchy(), knownGoals)
  const item = expediteMenuItem(held, expeditedId)
  if (!item) return []
  if (item.mode === 'choose') return [{ label: item.label, hint: item.hint, run: () => goalHost.pickGoal(m, held) }]
  const only = held[0]
  if (!only) return []
  return [{ label: item.label, run: () => expediteGoal(only).catch((e) => goalHost.say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) })) }]
}

// Stop expediting the room's goal, from the picker.
function stopExpediting(list: Goal[]) {
  const g = list.find((x) => x.id === get(expedite).goalId)
  if (g) expediteGoal(g).catch((e) => goalHost.say({ tone: 'error', subject: 'Could not expedite the goal', text: errorText(e) }))
}

/** The room's goals, named as the page named them when they lived there. */
export const goalView = {
  get goals() { return goals },
  get knownGoals() { return knownGoals },
  get rules() { return rules },
  get rulesEditing() { return rulesEditing },
  set rulesEditing(value: boolean) { rulesEditing = value },
  withItems,
  loadItems,
  /** A goals page replaces the list: a first page or an older one joined to it. */
  setGoals(rows: Goal[]) {
    goals = rows
  },
  loadGoals,
  loadRules,
  applyPendingRules,
  openGoal,
  goalById,
  trackFetchedGoal,
  refreshFetchedGoal,
  expediteGoal,
  expediteItems,
  stopExpediting,
  /** A room pick drops the old room's goals and rules. */
  clear() {
    goals = []
    fetchedGoal = null
    rules = null
    rulesPending = null
  },
}
