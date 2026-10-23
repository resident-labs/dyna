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
    expediteItems: (member: unknown) => call(fields, 'expediteItems', member),
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
