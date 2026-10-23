// Tests that slice App.svelte run its functions in one flat vm state. The toast
// line, the machine list and the room's goals moved to lib/app modules, so the
// same tests run those modules' functions there too. Each helper installs them,
// gives the state the object App reads (`toasts`, `machineView`, `goalView`) and
// binds the page's side to the state's own fields, as appRoomLoader.ts does for
// the room data.
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { get } from 'svelte/store'
import { moduleFunctions } from './svelte-source.ts'
import { expedite, expediteAction, heldGoals, isExpedited } from '../src/lib/rooms/expedite.ts'
import { expediteMenuItem } from '../src/lib/rooms/expediteMenu.ts'
import { nextRules } from '../src/lib/rooms/rules.ts'
import { errorText } from '../src/lib/errorText.ts'
import { clipText, dismissDelay, makeToast, TOAST_DETAIL_MS, TOAST_MS } from '../src/lib/ui/toast.ts'
import { reconcileRows } from '../src/lib/ui/reloadGate.ts'
import { questionID, visualKey } from '../src/lib/questions/questions.ts'

// The visual set is a .svelte.ts module with rune state at module scope, which
// plain Node cannot load; a test that needs it passes its own setter.
const setVisuals = (): void => {}
import { remoteList } from '../src/lib/lanesync/sync.ts'
import { mergeSessionPage } from '../src/lib/sessions/sessionPages.ts'
import { dmView } from '../src/lib/sessions/dm.ts'
import { prsBySession, isOpen } from '../src/lib/prs/pr.ts'

const read = (name: string) => readFileSync(new URL('../src/lib/app/' + name, import.meta.url), 'utf8')

const goalsSource = read('goals.svelte.ts')
const machinesSource = read('machines.svelte.ts')
const toastsSource = read('toasts.svelte.ts')

/** The goals module's own functions, which a vm can run in a flat state. */
export const GOALS_FUNCTIONS = [
  'withItems', 'loadItems', 'loadGoals', 'loadRules', 'applyPendingRules', 'openGoal', 'goalById',
  'trackFetchedGoal', 'refreshFetchedGoal', 'expediteGoal', 'expediteItems', 'stopExpediting',
] as const

/** The machines module's own functions, which a vm can run in a flat state. */
export const MACHINES_FUNCTIONS = [
  'machineVisible', 'loadMachines', 'loadMachine', 'openMachine', 'closeMachine', 'probeResult',
  'saveMachine', 'probeMachine', 'changed', 'clear',
] as const

/** The toasts module's own functions, which a vm can run in a flat state. */
export const TOAST_FUNCTIONS = [
  'say', 'hideToast', 'runToastAction', 'sayLine', 'sayDetails', 'toggleToastDetail', 'closeToastDetail',
] as const

/** moduleCode holds one module's functions as JavaScript, by name. */
export function moduleCode(source: string, names: readonly string[]): Map<string, string> {
  return moduleFunctions(source, names)
}

export const goalsCode = moduleCode(goalsSource, GOALS_FUNCTIONS)
export const machinesCode = moduleCode(machinesSource, MACHINES_FUNCTIONS)
export const toastsCode = moduleCode(toastsSource, TOAST_FUNCTIONS)

type State = Record<string, unknown>

const read_ = (state: State, name: string): unknown => Reflect.get(state, name)
const write = (state: State, name: string, value: unknown): void => { Reflect.set(state, name, value) }
const call = (state: State, name: string, ...args: unknown[]): unknown => {
  const fn = read_(state, name)
  if (typeof fn !== 'function') return undefined
  return (fn as (...values: unknown[]) => unknown)(...args)
}

function prepare(state: object, globals: Record<string, unknown>, source: string, names: readonly string[]): State {
  const fields = state as State
  if (!vm.isContext(state)) vm.createContext(state)
  for (const [name, value] of Object.entries(globals)) if (read_(fields, name) === undefined) write(fields, name, value)
  for (const [, code] of moduleFunctions(source, names)) vm.runInContext(code, state as vm.Context)
  return fields
}

/**
 * installGoals runs the goals module in the state and gives it the goalView
 * object App reads. The page's own side answers from the state's fields.
 */
export function installGoals(state: object): void {
  const fields = prepare(state, {
    get, expedite, expediteAction, heldGoals, isExpedited, expediteMenuItem, nextRules, clipText, errorText,
  }, goalsSource, GOALS_FUNCTIONS)
  write(fields, 'goalHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => {
      const gate = read_(fields, 'reload')
      if (typeof gate === 'function') return (gate as (n: string, l: unknown) => Promise<void>)(name, load)
      return Promise.resolve(load(() => true))
    },
    error: (message: string) => write(fields, 'loadError', message),
    openView: (view: string) => { if (typeof read_(fields, 'openView') === 'function') call(fields, 'openView', view); else write(fields, 'view', view) },
    selectGoal: (id: string) => write(fields, 'goalId', id),
    hidePins: () => write(fields, 'showPins', false),
    goalId: () => read_(fields, 'goalId'),
    askReplace: (goal: unknown) => write(fields, 'confirm', { kind: 'expedite', goal, roomId: read_(fields, 'roomId') }),
    pickGoal: (member: unknown, held: unknown) => write(fields, 'expediting', { member, goals: held }),
    hierarchy: () => read_(fields, 'hierarchy') ?? [],
    applyPage: (page: { items?: unknown[] }, append = false) => append
      ? [...((read_(fields, 'goals') as unknown[]) ?? []), ...(page.items ?? [])]
      : page.items ?? [],
    say: (input: unknown) => call(fields, 'say', input),
  })
  const goalsOf = () => (read_(fields, 'goals') as { id: string }[] | undefined) ?? []
  const knownGoals = () => {
    const list = goalsOf()
    const fetched = read_(fields, 'fetchedGoal') as { id: string } | null | undefined
    return fetched && !list.some((g) => g.id === fetched.id) ? [...list, fetched] : list
  }
  write(fields, 'goalView', {
    get goals() { return goalsOf() },
    get knownGoals() { return knownGoals() },
    get rules() { return read_(fields, 'rules') },
    get rulesEditing() { return !!read_(fields, 'rulesEditing') },
    set rulesEditing(value: boolean) { write(fields, 'rulesEditing', value) },
    withItems: (room: string, list: unknown[]) => call(fields, 'withItems', room, list),
    loadItems: (room: string, goal: unknown) => call(fields, 'loadItems', room, goal),
    loadGoals: () => call(fields, 'loadGoals'),
    loadRules: () => call(fields, 'loadRules'),
    applyPendingRules: () => call(fields, 'applyPendingRules'),
    openGoal: (id: string) => call(fields, 'openGoal', id),
    goalById: (id: string) => call(fields, 'goalById', id),
    trackFetchedGoal: (change: unknown) => call(fields, 'trackFetchedGoal', change),
    refreshFetchedGoal: (current?: () => boolean) => call(fields, 'refreshFetchedGoal', current),
    expediteGoal: (goal: unknown) => call(fields, 'expediteGoal', goal),
    expediteItems: (member: unknown, expeditedId: string) => call(fields, 'expediteItems', member, expeditedId),
    stopExpediting: (list: unknown[]) => call(fields, 'stopExpediting', list),
    setGoals: (rows: unknown[]) => write(fields, 'goals', rows),
    clear() {
      write(fields, 'goals', [])
      write(fields, 'fetchedGoal', null)
      write(fields, 'rules', null)
      write(fields, 'rulesPending', null)
    },
  })
}

/**
 * installMachines runs the machines module in the state and gives it the
 * machineView object App reads.
 */
export interface MachinesSlice {
  loadMachines(): Promise<void>
  loadMachine(id: string): Promise<unknown>
  openMachine(id: string): void
  closeMachine(): void
  probeMachine(): Promise<void>
}

/**
 * installMachines runs the machines module in the state and gives it the
 * machineView object App reads.
 */
export function installMachines(state: object): MachinesSlice {
  const fields = prepare(state, { errorText }, machinesSource, MACHINES_FUNCTIONS)
  write(fields, 'machineHost', {
    listShown: () => !!call(fields, 'machinesVisible'),
    screenShown: () => read_(fields, 'pageVisible') !== false,
    narrow: () => !!read_(fields, 'narrow'),
    setDrawer: (open: boolean) => write(fields, 'navOpen', open),
    say: (input: unknown) => call(fields, 'say', input),
  })
  const view = {
    get machines() { return read_(fields, 'machines') },
    get machineId() { return read_(fields, 'machineId') },
    get machineDetail() { return read_(fields, 'machineDetail') },
    get machineError() { return read_(fields, 'machineError') },
    get machineBusy() { return read_(fields, 'machineBusy') },
    get machineProbing() { return read_(fields, 'machineProbing') },
    visible: () => call(fields, 'machineVisible'),
    changed: () => call(fields, 'changed'),
    clear: () => call(fields, 'clear'),
    loadMachines: () => call(fields, 'loadMachines'),
    loadMachine: (id: string) => call(fields, 'loadMachine', id),
    openMachine: (id: string) => call(fields, 'openMachine', id),
    closeMachine: () => call(fields, 'closeMachine'),
    saveMachine: (patch: unknown) => call(fields, 'saveMachine', patch),
    probeMachine: () => call(fields, 'probeMachine'),
  }
  write(fields, 'machineView', view)
  return view as MachinesSlice
}

/**
 * installToasts runs the toasts module in the state and gives it the toasts
 * object App reads.
 */
export function installToasts(state: object): void {
  const fields = prepare(state, { makeToast, dismissDelay, TOAST_DETAIL_MS, TOAST_MS }, toastsSource, TOAST_FUNCTIONS)
  write(fields, 'toasts', {
    get line() { return read_(fields, 'toast') },
    get detail() { return read_(fields, 'toastDetail') },
    get detailOpen() { return read_(fields, 'toastDetailOpen') },
    say: (input: unknown) => call(fields, 'say', input),
    hideToast: () => call(fields, 'hideToast'),
    runToastAction: (run: () => void) => call(fields, 'runToastAction', run),
    sayLine: (message: string) => call(fields, 'sayLine', message),
    sayDetails: (summary: unknown) => call(fields, 'sayDetails', summary),
    toggleToastDetail: () => call(fields, 'toggleToastDetail'),
    closeToastDetail: () => call(fields, 'closeToastDetail'),
  })
}

// ---- Part 2: questions, pins, issues, sync counts and pull requests ----

const questionsSource = read('questions.svelte.ts')
const pinsSource = read('pins.svelte.ts')
const issuesSource = read('issues.svelte.ts')
const syncSource = read('sync.svelte.ts')
const prsSource = read('prs.svelte.ts')

/** The questions module's own functions, which a vm can run in a flat state. */
export const QUESTIONS_FUNCTIONS = ['loadQuestions', 'loadVisuals', 'answerQuestion', 'openQuestion'] as const
/** The pins module's own functions, which a vm can run in a flat state. */
export const PINS_FUNCTIONS = ['loadPins', 'togglePin'] as const
/** The issues module's own functions, which a vm can run in a flat state. */
export const ISSUES_FUNCTIONS = ['loadIssues', 'openIssues', 'triageIssue', 'askCloseIssue'] as const
/** The sync module's own functions, which a vm can run in a flat state. */
export const SYNC_FUNCTIONS = ['loadSync', 'syncAction'] as const
/** The pull requests module's own functions, which a vm can run in a flat state. */
export const PRS_FUNCTIONS = ['loadReviewer', 'reviewerSoon', 'loadChanges', 'untrackPR', 'trackPR', 'resumePR'] as const

export const questionsCode = moduleCode(questionsSource, QUESTIONS_FUNCTIONS)
export const pinsCode = moduleCode(pinsSource, PINS_FUNCTIONS)
export const issuesCode = moduleCode(issuesSource, ISSUES_FUNCTIONS)
export const syncCode = moduleCode(syncSource, SYNC_FUNCTIONS)
export const prsCode = moduleCode(prsSource, PRS_FUNCTIONS)

/** The questions module's rows, as App reads them through questionView. */
export interface QuestionsSlice {
  loadQuestions(): Promise<void>
  loadVisuals(): Promise<void>
}

/** installQuestions runs the questions module in the state and gives it the questionView object. */
export function installQuestions(state: object): QuestionsSlice {
  const fields = prepare(state, { errorText, reconcileRows, questionID, visualKey, setVisuals }, questionsSource, QUESTIONS_FUNCTIONS)
  write(fields, 'questionsHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => gate(fields, name, load),
    error: (message: string) => write(fields, 'loadError', message),
    openView: (view: string) => { if (typeof read_(fields, 'openView') === 'function') call(fields, 'openView', view); else write(fields, 'view', view) },
  })
  const list = () => (read_(fields, 'questions') as unknown[] | undefined) ?? []
  const view = {
    get questions() { return list() },
    get visuals() { return read_(fields, 'visuals') ?? [] },
    get focus() { return read_(fields, 'questionFocus') ?? '' },
    get focusRequest() { return read_(fields, 'questionFocusRequest') ?? 0 },
    loadQuestions: () => call(fields, 'loadQuestions'),
    loadVisuals: () => call(fields, 'loadVisuals'),
    answerQuestion: (id: string, choice: string, text: string) => call(fields, 'answerQuestion', id, choice, text),
    openQuestion: (post: unknown) => call(fields, 'openQuestion', post),
    setQuestions: (rows: unknown[]) => write(fields, 'questions', rows),
    clear() {
      write(fields, 'questions', [])
      write(fields, 'visuals', [])
      write(fields, 'questionFocus', '')
      write(fields, 'questionsRequest', 0)
    },
  }
  write(fields, 'questionView', view)
  return view as QuestionsSlice
}

/** installPins runs the pins module in the state and gives it the pinView object. */
export function installPins(state: object): void {
  const fields = prepare(state, { reconcileRows }, pinsSource, PINS_FUNCTIONS)
  write(fields, 'pinsHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => gate(fields, name, load),
    error: (message: string) => write(fields, 'loadError', message),
    clearError: () => write(fields, 'loadError', ''),
  })
  write(fields, 'pinView', {
    get pins() { return read_(fields, 'pins') ?? [] },
    get ids() { return new Set(((read_(fields, 'pins') as { id: string }[] | undefined) ?? []).map((p) => p.id)) },
    loadPins: () => call(fields, 'loadPins'),
    togglePin: (post: unknown) => call(fields, 'togglePin', post),
    setPins: (rows: unknown[]) => write(fields, 'pins', rows),
    clear() { write(fields, 'pins', []) },
  })
}

/** installIssues runs the issues module in the state and gives it the issueView object. */
export function installIssues(state: object): void {
  const fields = prepare(state, { errorText, reconcileRows }, issuesSource, ISSUES_FUNCTIONS)
  write(fields, 'issuesHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => gate(fields, name, load),
    error: (message: string) => write(fields, 'loadError', message),
    openIssuesView: () => call(fields, 'openIssuesView'),
    ask: (prompt: unknown) => write(fields, 'ask', prompt),
    clearAsk: () => write(fields, 'ask', null),
    say: (input: unknown) => call(fields, 'say', input),
  })
  write(fields, 'issueView', {
    get issues() { return read_(fields, 'issues') ?? [] },
    get issueHistory() { return read_(fields, 'issueHistory') ?? [] },
    loadIssues: (history?: boolean) => call(fields, 'loadIssues', history),
    openIssues: () => call(fields, 'openIssues'),
    triageIssue: (fn: () => Promise<unknown>) => call(fields, 'triageIssue', fn),
    askCloseIssue: (issue: unknown) => call(fields, 'askCloseIssue', issue),
    clear() {
      write(fields, 'issues', [])
      write(fields, 'issueHistory', [])
    },
  })
}

/** The sync module's rows, as App reads them through syncView. */
export interface SyncSlice {
  loadSync(): Promise<void>
  syncAction(call: unknown): Promise<void>
}

/** installSync runs the sync module in the state and gives it the syncView object. */
export function installSync(state: object): SyncSlice {
  const fields = prepare(state, { errorText, remoteList }, syncSource, SYNC_FUNCTIONS)
  write(fields, 'syncHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => gate(fields, name, load),
  })
  const view = {
    get repo() { return read_(fields, 'syncRepo') ?? null },
    get remotes() { return read_(fields, 'syncRemotes') ?? [] },
    get note() { return read_(fields, 'syncNote') ?? '' },
    loadSync: () => call(fields, 'loadSync'),
    syncAction: (fn: unknown) => call(fields, 'syncAction', fn),
  }
  write(fields, 'syncView', view)
  return view as SyncSlice
}

/** installPRs runs the pull requests module in the state and gives it the changeView object. */
export function installPRs(state: object): void {
  const fields = prepare(state, { errorText, prsBySession, isOpen, setTimeout, clearTimeout, reviewerTimer: undefined }, prsSource, PRS_FUNCTIONS)
  write(fields, 'prsHost', {
    reload: (name: string, load: (current: () => boolean) => unknown) => gate(fields, name, load),
    applyPage: (page: unknown) => call(fields, 'applyListPage', 'changes', page),
    alive: () => read_(fields, 'consolidationAlive') !== false,
    say: (input: unknown) => call(fields, 'say', input),
  })
  write(fields, 'changeView', {
    get changes() { return read_(fields, 'changes') ?? null },
    get reviewer() { return read_(fields, 'reviewer') ?? null },
    get mergeVersion() { return read_(fields, 'mergeVersion') ?? 0 },
    get error() { return read_(fields, 'prError') ?? '' },
    get prs() { return read_(fields, 'prs') ?? [] },
    get bySession() { return read_(fields, 'prMap') ?? {} },
    get openBySession() { return read_(fields, 'openPRMap') ?? {} },
    loadReviewer: () => call(fields, 'loadReviewer'),
    reviewerSoon: () => call(fields, 'reviewerSoon'),
    loadChanges: () => call(fields, 'loadChanges'),
    untrackPR: (row: unknown) => call(fields, 'untrackPR', row),
    trackPR: (number: unknown, member: unknown) => call(fields, 'trackPR', number, member),
    resumePR: (row: unknown) => call(fields, 'resumePR', row),
    bumpVersion() { write(fields, 'mergeVersion', Number(read_(fields, 'mergeVersion') ?? 0) + 1) },
    setChanges: (rows: unknown[]) => write(fields, 'changes', rows),
    clear() {
      write(fields, 'changes', null)
      write(fields, 'reviewer', null)
    },
  })
}

// gate runs one module load through the test's own reload gate when it has one.
function gate(fields: State, name: string, load: (current: () => boolean) => unknown): Promise<void> {
  const reload = read_(fields, 'reload')
  if (typeof reload === 'function') return (reload as (n: string, l: unknown) => Promise<void>)(name, load)
  return Promise.resolve(load(() => true)) as Promise<void>
}

// ---- Part 3: the open session's transcript ----

const sessionViewSource = read('sessionView.svelte.ts')

/** The session view's own functions, which a vm can run in a flat state. */
export const SESSION_VIEW_FUNCTIONS = [
  'sessionVisible', 'showSession', 'loadSession', 'loadEarlierSession', 'loadNewerSession',
  'presentSession', 'unqueue', 'dismissPendingPost', 'setSession',
] as const

export const sessionViewCode = moduleCode(sessionViewSource, SESSION_VIEW_FUNCTIONS)

/** The session view's rows, as App reads them through transcripts. */
export interface SessionViewSlice {
  session: unknown
  queue: unknown[]
  error: string
  threadCount: number
  readingHistory: boolean
  visit: number
  nextVisit(): void
  openFresh(): void
  clear(): void
  setSession(value: unknown): void
  patchMember(member: unknown): void
  sessionVisible(tab?: string): boolean
  loadSession(): Promise<void>
  showSession(value: unknown, older?: boolean | string): void
  schedule(): void
  startApp(): void
  destroy(): void
}

/**
 * installSessionView runs the session view module in the state and gives it the
 * transcripts object App reads. The page's side answers from the state, and the
 * state's own `sessionData` controller answers the transcript requests.
 */
export function installSessionView(state: object, data?: unknown): SessionViewSlice {
  const fields = prepare(state, { mergeSessionPage, dmView, setTimeout, clearTimeout }, sessionViewSource, SESSION_VIEW_FUNCTIONS)
  write(fields, 'sessionHost', {
    pageOpen: () => read_(fields, 'pageVisible') !== false && !(read_(fields, 'narrow') && read_(fields, 'reviewOpen')),
    tab: () => String(read_(fields, 'sessionTab') ?? 'chat'),
    say: (input: unknown) => call(fields, 'say', input),
  })
  // The controller is created at module scope, so the flat state supplies one.
  const controller = data ?? read_(fields, 'sessionData') ?? {
    load: () => Promise.resolve(), earlier: () => Promise.resolve(false), newer: () => Promise.resolve(false),
    present: () => Promise.resolve(false), schedule: () => {}, start: () => {}, destroy: () => {},
  }
  write(fields, 'data', controller)
  const view: SessionViewSlice = {
    get session(): unknown { return read_(fields, 'session') ?? null },
    get queue(): unknown[] { return (read_(fields, 'queue') as unknown[] | undefined) ?? [] },
    get error(): string { return String(read_(fields, 'sessionError') ?? '') },
    get threadCount(): number { return Number(read_(fields, 'threadCount') ?? 0) },
    get readingHistory(): boolean { return !!read_(fields, 'sessionReadingHistory') },
    set readingHistory(value: boolean) { write(fields, 'sessionReadingHistory', value) },
    get visit(): number { return Number(read_(fields, 'sessionVisit') ?? 0) },
    nextVisit() { write(fields, 'sessionVisit', Number(read_(fields, 'sessionVisit') ?? 0) + 1) },
    openFresh() {
      write(fields, 'session', null)
      write(fields, 'sessionError', '')
      write(fields, 'queue', [])
    },
    clear() { write(fields, 'session', null) },
    setSession: (value: unknown) => call(fields, 'setSession', value),
    patchMember(member: unknown) {
      const current = read_(fields, 'session') as { member?: { id?: string } } | null | undefined
      if (current && current.member?.id === (member as { id?: string } | null)?.id) write(fields, 'session', { ...current, member })
    },
    sessionVisible: (tab?: string) => !!call(fields, 'sessionVisible', tab),
    loadSession: () => call(fields, 'loadSession') as Promise<void>,
    showSession: (value: unknown, older?: boolean | string) => call(fields, 'showSession', value, older),
    schedule() { (controller as { schedule?(): void }).schedule?.() },
    startApp() { (controller as { start?(): void }).start?.() },
    destroy() { (controller as { destroy?(): void }).destroy?.() },
  }
  write(fields, 'transcripts', view)
  return view
}
