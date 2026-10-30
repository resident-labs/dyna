import { actions, actionItems, roomMenu } from '../ui/actions.ts'
import { paletteTargets } from '../ui/paletteTargets.ts'
import { roomLabel } from '../ui/format.ts'
import type { ActionContext } from '../ui/actions.ts'
import type { Target } from '../ui/paletteTargets.ts'
import type { Room, Member } from '../api/room.ts'
import type { RunList } from '../api/workflow.ts'
import type { Row } from '../prs/prlist.ts'

const ICON = {
  files: 'M3 5.5a1.5 1.5 0 0 1 1.5-1.5h3.4l1.6 1.8h6a1.5 1.5 0 0 1 1.5 1.5v7.2a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 3 14.5Z',
  terminal: 'M3.5 4h13a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1ZM6 8l2.5 2L6 12M10.5 12.5H14',
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

interface Host {
  room(): Room | null | undefined
  roomId(): string
  rooms(): Room[]
  runs(): RunList | null
  members(): Member[]
  changes(): Row[] | null
  session(): ActionContext['session']
  sessionPRs(): ActionContext['sessionPRs']
  narrow(): boolean
  hasTerminal(): boolean
  openView: ActionContext['openView']
  openSettings: ActionContext['openSettings']
  renameRoom: ActionContext['renameRoom']
  deleteRoom: ActionContext['deleteRoom']
  roomAction: ActionContext['roomAction']
  say: ActionContext['say']
  loadRooms(): Promise<unknown>
  selectRoom(id: string): Promise<boolean>
  openFiles(path?: string): void
  openPins(): void
  openRoomSettings: ActionContext['openRoomSettings']
  openSessionPRs: ActionContext['openSessionPRs']
  cancelRun: ActionContext['cancelRun']
  toggleSidebar: ActionContext['toggleSidebar']
  startWorkflowDialog(): void
  openSession(session: Extract<Target, { kind: 'session' }>['session']): void
  openRun(run: { id: string }): void
  openPR(pr: string): void
}

export function createPalette(host: Host) {
  let show = $state(false)
  let path = $state<string | null>(null)
  let query = $state('')

  function openSearch(value = '') { query = value; show = true }
  function closeSearch() { show = false; path = null; query = '' }
  function openNewRoom(value = '') { path = value; show = true }

  function registry(room = host.room()) {
    const rooms = host.rooms()
    const context: ActionContext = {
      room: room ?? null, rooms, icons: ICON, hasTerminal: host.hasTerminal(), narrow: host.narrow(),
      runs: host.runs(), session: host.session(), sessionPRs: host.sessionPRs(),
      openView: host.openView, openSettings: host.openSettings, renameRoom: host.renameRoom,
      deleteRoom: host.deleteRoom, roomAction: host.roomAction, say: host.say, loadRooms: host.loadRooms,
      openFiles: host.openFiles, openPins: host.openPins,
      openSearch: (arg) => openSearch(arg || ''), openRoomSettings: host.openRoomSettings,
      openSessionPRs: host.openSessionPRs, cancelRun: host.cancelRun, toggleSidebar: host.toggleSidebar,
      roomOptions: () => host.rooms().filter((x) => x.id !== room?.id).map((x) => ({ value: x.name, hint: x.displayName && x.displayName !== x.name ? x.displayName : x.repo || '' })),
      switchRoom: (arg) => {
        if (!arg) { show = true; return }
        const a = arg.toLowerCase()
        const hit = host.rooms().find((x) => x.name.toLowerCase() === a || roomLabel(x).toLowerCase() === a) ?? host.rooms().find((x) => `${x.name} ${roomLabel(x)}`.toLowerCase().includes(a))
        if (!hit) return `No room matches "${arg}".`
        host.selectRoom(hit.id)
      },
      newRoom: openNewRoom,
      startWorkflow: () => { host.openView('runs'); host.startWorkflowDialog() },
    }
    return actions(context)
  }

  return {
    get show() { return show },
    get path() { return path },
    get query() { return query },
    get targets() {
      if (!show || !host.roomId()) return []
      return paletteTargets(host.members().filter((m): m is Member & { id: string } => !!m.id), host.runs()?.runs ?? [], (host.changes() ?? []).filter((c): c is Row & { changeId: string } => !!c.changeId))
    },
    openSearch, closeSearch, openNewRoom, registry,
    get paletteCommands() { return actionItems(registry(), 'palette') },
    get composerCommands() {
      return actionItems(registry(), 'slash').map((item) => ({
        ...item,
        run: (arg?: string) => registry().find((action) => action.id === item.id)?.run(arg),
      }))
    },
    roomMenu(room: Room) { return roomMenu(registry(room)) },
    openTarget(t: Target) {
      if (t.kind === 'session') host.openSession(t.session)
      else if (t.kind === 'run') host.openRun(t.run)
      else host.openPR(t.pr)
    },
    async openRoom(id: string) { await host.loadRooms(); host.selectRoom(id) },
  }
}
