// The open room's agent computers: the list the computers page and the member
// rows read, each session's recent actions, and the preview the open session
// shows. lib/computers holds the shapes and the stream rules; this module keeps
// the state and the requests, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { applyEvent, openedApps, pushAction } from '../computers/computers.ts'
import { previewVisible } from '../computers/preview.ts'
import { router } from './router.svelte.ts'
import type { ComputerEvent, ComputerInfo } from '../api/computer.ts'

/** The page's own side of the computers. App binds these once. */
export interface ComputersHost {
  /** The page's reload gate, so the list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** The open session's tab, which decides whether the preview shows. */
  tab(): string
  /** The window width, which decides whether the preview shows. */
  width(): number
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`computers: ${name} is unbound`) }
let computersHost: ComputersHost = {
  reload: () => missing('reload'),
  tab: () => missing('tab'),
  width: () => missing('width'),
}

export function bindComputers(bound: ComputersHost): void {
  computersHost = bound
}

const roomId = $derived(router.roomId)
const drill = $derived(router.drill)

// Agent computers of the open room (null when the feature is unavailable),
// each session's recent actions, and whether the viewer is open.
let computers = $state<ComputerInfo[] | null>(null)
let computerFeed = $state<ReturnType<typeof pushAction>>({})
let computerError = $state('')
let computerRequest = 0
let computerTimer: ReturnType<typeof setTimeout> | undefined

function loadComputers() { return computersHost.reload('computers', async (current: () => boolean) => {
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

function computerOf(id?: string) { return computers?.find((c) => c.session === id) ?? null }

// The floating preview and the phone chip. The preview shows in a
// session's Messages view only, never opens a stream, and opens the Computer tab on a click.
const drillComputer = $derived(drill ? computerOf(drill.computer) : null)
// view is '': a session view is open, so the computers page is not.
const showPreview = $derived(!!drill && !!drillComputer && previewVisible({ c: drillComputer, tab: computersHost.tab(), view: '', width: computersHost.width(), hidden: null }))
// The computer the session tab row offers: none until the room's computers
// load. The engine names each row's computer, root's included.
const sessionComputer = $derived(computers && drill ? computerOf(drill.computer) ?? { state: 'none' } : null)

// clear leaves the previous room's computers, as a room pick does.
function clear() {
  computers = null
  computerFeed = {}
}

/** The computer views, named as the page named them when they lived there. */
export const computerView = {
  get computers() { return computers },
  get feed() { return computerFeed },
  get error() { return computerError },
  get drillComputer() { return drillComputer },
  get sessionComputer() { return sessionComputer },
  get showPreview() { return showPreview },
  computerOf,
  loadComputers,
  handleComputer,
  clear,
}
