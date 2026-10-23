// The machine list and one machine's page. The rail's machines tile lists the
// engine's machines at the load card's cadence, and a pick opens that machine's
// page in the main area. lib/api/machine.ts holds the shapes and MachinePage
// draws the page; this module keeps the state and the requests, so App.svelte
// only hosts them.
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { LOAD_POLL_MS } from '../system/systemLoad.ts'
import type { MachineDetail, MachinePatch, MachineSummary } from '../api/machine.ts'
import type { ToastInput } from '../ui/toast.ts'

/** The page's own side of the machine views. App binds these once. */
export interface MachinesHost {
  /** The sidebar list shows, so its figures keep current. */
  listShown(): boolean
  /** The window shows this page at all. */
  screenShown(): boolean
  /** The screen is narrow, so the sidebar is a drawer over the page. */
  narrow(): boolean
  /** Open or close the phone drawer. */
  setDrawer(open: boolean): void
  /** Say one line about a finished action. */
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`machines: ${name} is unbound`) }
let machineHost: MachinesHost = {
  listShown: () => missing('listShown'),
  screenShown: () => missing('screenShown'),
  narrow: () => missing('narrow'),
  setDrawer: () => missing('setDrawer'),
  say: () => missing('say'),
}

export function bindMachines(bound: MachinesHost): void {
  machineHost = bound
}

let machines = $state<MachineSummary[] | null>(null) // null until the first answer
let machinesRequest = 0
let machineId = $state('') // the machine whose page is open, or ''
let machineDetail = $state<MachineDetail | null>(null)
let machineError = $state('')
let machineBusy = $state(false)
let machineProbing = $state(false)
let machineRequest = 0

function machineVisible() { return machineHost.screenShown() && machineId !== '' }

// loadMachines reads the machine list now. A slow answer never replaces a
// newer one.
function loadMachines() {
  const request = ++machinesRequest
  return api.machines().then((list) => { if (request === machinesRequest) machines = list }, () => {})
}

// loadMachine reads the open machine page. A slow answer never replaces a
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
  machineHost.setDrawer(false)
  machineError = ''
  machineBusy = false
  loadMachine(id)
}

function closeMachine() {
  machineId = ''
  machineDetail = null
  machineError = ''
  if (machineHost.narrow()) machineHost.setDrawer(true)
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
    machineHost.say(probeResult(detail))
  } catch {
    if (machineId === id) machineHost.say({ tone: 'error', subject: `Could not probe ${name}` })
  } finally {
    machineProbing = false
  }
}

// watchMachines keeps the figures current. They move on their own, so the
// list and the open page read again at the load card's cadence. A phone keeps
// the page while the drawer is shut, so the page reads on its own terms, not
// the sidebar's.
export function watchMachines(): void {
  $effect(() => {
    const list = machineHost.listShown()
    const open = machineVisible()
    if (!list && !open) return
    const tick = () => {
      if (list) loadMachines()
      if (open && !machineBusy && !machineProbing) loadMachine(machineId)
    }
    tick()
    const timer = setInterval(tick, LOAD_POLL_MS)
    return () => clearInterval(timer)
  })
}

// changed rereads what a machine event can change: the list that shows and
// the page that is open.
function changed() {
  if (machineHost.listShown()) loadMachines()
  if (machineId) loadMachine(machineId)
}

// clear leaves the open machine's page, as a room pick does.
function clear() {
  machineId = ''
  machineDetail = null
  machineError = ''
}

/** The machine views, named as the page named them when they lived there. */
export const machineView = {
  get machines() { return machines },
  get machineId() { return machineId },
  get machineDetail() { return machineDetail },
  get machineError() { return machineError },
  get machineBusy() { return machineBusy },
  get machineProbing() { return machineProbing },
  /** A machine page shows: the window is visible and one machine is open. */
  visible() { return machineVisible() },
  changed,
  clear,
  loadMachines,
  loadMachine,
  openMachine,
  closeMachine,
  saveMachine,
  probeMachine,
}
