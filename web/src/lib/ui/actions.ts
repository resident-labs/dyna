import { api } from '../../api.ts'
import { roomLabel } from './format.ts'
import type { Room } from '../api/room.ts'

export type ActionRoom = Pick<Room, 'id' | 'name'> &
  Partial<Pick<Room, 'displayName' | 'repo' | 'pinned' | 'archived' | 'pinOrder'>>

interface ActionRun { id: string; name: string; status: string }
type Handler = (arg?: string) => unknown
interface Option { value: string; hint: string }
interface Surfaces {
  palette: { label: string; hint?: string; icon?: string; terms: string; danger?: boolean }
  slash: { name: string; arg?: string; listTitle?: string; hint: string; pickRuns?: boolean; options?: () => Option[] }
  menu: { label: string; hint?: string }
}
export type Action = { id: string; run: Handler } & {
  [K in keyof Surfaces]?: Surfaces[K] | null
}
export interface ActionContext {
  room: ActionRoom | null
  rooms: Partial<Pick<Room, 'pinOrder'>>[]
  icons: Partial<Record<'files' | 'github' | 'terminal' | 'jobs' | 'run' | 'stop' | 'flows' | 'pin' | 'find' | 'gear' | 'rename' | 'archive' | 'trash' | 'device' | 'side', string>>
  hasTerminal: boolean
  narrow?: boolean
  runs?: { runs?: ActionRun[] | null } | null
  session?: { name: string } | null
  sessionPRs?: readonly unknown[] | null
  roomOptions?: () => Option[]
  templateOptions?: () => Option[]
  switchRoom: Handler
  newRoom: Handler
  openFiles: Handler
  startWorkflow: Handler
  openPins: Handler
  openSearch: Handler
  openSessionPRs: Handler
  toggleSidebar: Handler
  openView: (view: string) => unknown
  openSettings: (section?: string, pairNow?: boolean) => unknown
  cancelRun: (run: ActionRun) => unknown
  openRoomSettings: (room: ActionRoom) => unknown
  renameRoom: (room: ActionRoom) => unknown
  deleteRoom: (room: ActionRoom) => unknown
  say: (message: string) => unknown
  loadRooms: () => unknown
  roomAction: (run: () => Promise<unknown>, note: string) => unknown
}

// Each action owns its handler and availability. The three surfaces only
// choose presentation metadata; they never infer behavior from a label.
export function actions(c: ActionContext): Action[] {
  const out: Action[] = []
  const r = c.room
  const icon = c.icons
  if (r) {
    const label = roomLabel(r)
    out.push({ id: 'room', slash: { name: 'room', arg: 'room name', listTitle: 'Rooms', hint: 'switch to another room', pickRuns: true, options: c.roomOptions }, run: c.switchRoom })
    out.push({ id: 'new', slash: { name: 'new', arg: 'folder path', hint: 'make a room for a repo' }, run: c.newRoom })
    if (r.repo) out.push(
      { id: 'files', palette: { label: 'Open files', hint: label, icon: icon.files, terms: 'browse code repo tree explorer' }, slash: { name: 'files', arg: 'path', hint: 'open the repo files' }, run: c.openFiles },
      { id: 'github', palette: { label: 'Open pull requests', hint: 'GitHub', icon: icon.github, terms: 'pr pull requests github review' }, slash: { name: 'prs', hint: 'pull requests on GitHub' }, run: () => c.openView('github') },
      { id: 'issues', palette: { label: 'Open issues', hint: 'GitHub', icon: icon.github, terms: 'issues bugs github tickets' }, slash: { name: 'issues', hint: 'issues on GitHub' }, run: () => c.openView('issues') },
    )
    if (c.hasTerminal) out.push({ id: 'terminal', palette: { label: 'Open terminal', hint: `in ${label}`, icon: icon.terminal, terms: 'shell console bash command' }, slash: { name: 'terminal', hint: 'a shell in the repo' }, run: () => c.openView('terminal') })
    out.push(
      { id: 'jobs', palette: { label: 'Open shell jobs', icon: icon.jobs, terms: 'dyna shell background log build test' }, slash: { name: 'jobs', hint: 'dyna shell jobs and logs' }, run: () => c.openView('jobs') },
      { id: 'run', palette: { label: 'Run a workflow…', icon: icon.run, terms: 'start workflow template run' }, slash: { name: 'run', arg: 'template and input', listTitle: 'Workflow templates', hint: 'start a workflow', options: c.templateOptions }, run: c.startWorkflow },
    )
    for (const run of (c.runs?.runs ?? []).filter((x) => x.status === 'running')) out.push({ id: `cancel-${run.id}`, palette: { label: `Cancel ${run.name}`, hint: 'running workflow', icon: icon.stop, danger: true, terms: 'cancel stop workflow run' }, run: () => c.cancelRun(run) })
    out.push(
      { id: 'workflows', palette: { label: 'Open workflows', icon: icon.flows, terms: 'runs run workflow' }, slash: { name: 'workflows', hint: 'runs in this room' }, run: () => c.openView('runs') },
      { id: 'pins', palette: { label: 'Open pinned posts', icon: icon.pin, terms: 'pin pinned saved' }, run: c.openPins },
      { id: 'find', slash: { name: 'find', arg: 'text', hint: 'search sessions and every post in this room' }, run: c.openSearch },
      { id: 'roomsettings', palette: { label: 'Room settings…', hint: label, icon: icon.gear, terms: 'room settings review reviewer landing ci budget hooks rules pause publishing noise' }, menu: { label: 'Room settings…' }, run: () => c.openRoomSettings(r) },
    )
    if (c.session && c.sessionPRs?.length) out.push({ id: 's-prs', palette: { label: `Pull requests of ${c.session.name}`, icon: icon.flows, terms: 'pr pull request ci review' }, run: c.openSessionPRs })
    out.push(
      { id: 'rename', palette: { label: 'Rename room…', hint: label, icon: icon.rename, terms: 'room name title' }, menu: { label: 'Rename…' }, slash: { name: 'rename', arg: 'new name', hint: 'rename this room' }, run: async (arg) => {
        if (!arg) return void c.renameRoom(r)
        await api.renameRoom(r.id, arg)
        c.say(`Renamed to ${arg}.`)
        c.loadRooms()
      } },
      { id: 'room-pin', palette: { label: r.pinned ? 'Unpin from the rail' : 'Pin to the rail', hint: label, icon: icon.pin, terms: 'room pin unpin rail' }, menu: { label: r.pinned ? 'Unpin from rail' : 'Pin to rail' }, slash: { name: r.pinned ? 'unpin' : 'pin', hint: r.pinned ? 'take this room off the rail top' : 'pin this room to the rail' }, run: () => c.roomAction(
        () => r.pinned ? api.unpinRoom(r.id) : api.pinRoom(r.id, Math.max(0, ...c.rooms.map((x) => x.pinOrder || 0)) + 1),
        r.pinned ? `${label} is no longer pinned.` : `${label} is pinned to the rail.`,
      ) },
      { id: 'room-archive', palette: { label: r.archived ? 'Unarchive' : 'Archive', hint: label, icon: icon.archive, terms: 'room archive unarchive hide rail' }, menu: { label: r.archived ? 'Unarchive' : 'Archive', hint: r.archived ? '' : 'hides it from the rail' }, slash: { name: r.archived ? 'unarchive' : 'archive', hint: r.archived ? 'put this room back on the rail' : 'hide this room from the rail' }, run: () => c.roomAction(
        () => r.archived ? api.unarchiveRoom(r.id) : api.archiveRoom(r.id),
        r.archived ? `${label} is back on the rail.` : `${label} is archived. Find it on the home screen.`,
      ) },
      { id: 'delete', palette: { label: 'Delete room…', hint: label, icon: icon.trash, danger: true, terms: 'room remove delete' }, run: () => c.deleteRoom(r) },
    )
  }
  out.push(
    { id: 'pair', palette: { label: 'Pair a device', hint: 'code and QR', icon: icon.device, terms: 'phone pair qr connect device' }, slash: r ? { name: 'pair', hint: 'pair a phone with a code and QR' } : null, run: () => c.openSettings('devices', true) },
    { id: 'devices', palette: { label: 'Manage devices', hint: 'revoke access', icon: icon.device, terms: 'devices phones revoke sessions connect' }, run: () => c.openSettings('devices') },
    { id: 'settings', palette: { label: 'Open Settings', icon: icon.gear, terms: 'preferences config profiles' }, run: () => c.openSettings() },
  )
  out.push({ id: 'sidebar', palette: { label: 'Toggle sidebar', hint: c.narrow ? '' : 'Ctrl+B', icon: icon.side, terms: 'side panel hide show drawer' }, run: c.toggleSidebar })
  return out
}

export function actionItems<K extends keyof Surfaces>(registry: Action[], surface: K) {
  return registry.flatMap((action) => {
    const metadata = action[surface]
    return metadata ? [{ id: action.id, ...metadata, run: action.run }] : []
  })
}

export function roomMenu(registry: Action[]) {
  const items = actionItems(registry, 'menu')
  return ['rename', 'room-pin', 'room-archive', 'roomsettings'].flatMap((id) => {
    const item = items.find((item) => item.id === id)
    return item ? [item] : []
  })
}
