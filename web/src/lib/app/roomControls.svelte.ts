import { createRoomSessionControls, roomControlLabels, roomControlSummary } from '../rooms/roomSessionControls.ts'
import type { RoomControlsApi, RoomControlValue, RoomControlSummary, SessionName } from '../rooms/roomSessionControls.ts'

interface RoomContext { roomId: string; visit: number }
interface Host {
  api: RoomControlsApi
  room: () => string
  request: () => number
  alive: () => boolean
  visible: () => boolean
  hierarchy: () => SessionName[]
  refresh: () => void
  error: (subject: string, text: string) => void
  details: (summary: RoomControlSummary) => void
  create?: typeof createRoomSessionControls
}

export function createRoomControls(host: Host) {
  let controller: ReturnType<typeof createRoomSessionControls> | null = null
  let value = $state<RoomControlValue | null>(null)
  let menu = $state<(RoomContext & { x: number; y: number; anchor?: HTMLElement }) | null>(null)
  let confirm = $state<(RoomContext & { count: number }) | null>(null)
  let visitNumber = 0
  let reported = ''
  let alive = true

  function visit(id: string) {
    controller?.destroy()
    controller = null
    value = null
    menu = null
    confirm = null
    const visit = ++visitNumber, request = host.request()
    if (!id) return
    controller = (host.create ?? createRoomSessionControls)(host.api, id, (next) => {
      if (!alive || !host.alive() || id !== host.room() || request !== host.request() || visit !== visitNumber) return
      const finished = value?.busy && !next.busy
      value = next
      if (finished) host.refresh()
    }, host.visible)
  }
  function current(context: RoomContext) {
    return alive && host.alive() && context.roomId === host.room() && context.visit === visitNumber
  }
  function askClear(count?: number) {
    if (!alive || !host.alive() || !host.room() || value?.busy) return
    const n = count || value?.state?.dormant || 0
    if (n) confirm = { roomId: host.room(), visit: visitNumber, count: n }
  }
  function items(context: RoomContext) {
    const busy = !value?.state || value.busy
    const dormant = value?.state?.dormant || 0
    return [
      { label: 'Pause all', hint: 'let running turns finish', disabled: busy || value?.state?.paused, run: () => { if (current(context)) controller?.run('pause') } },
      { label: 'Resume all', disabled: busy, run: () => { if (current(context)) controller?.run('resume') } },
      { label: 'Clear dormant sessions…', gap: true, disabled: busy || !dormant, run: () => { if (current(context)) askClear(dormant) } },
    ]
  }
  function report() {
    if (!value) return
    if (value.busy) { reported = ''; return }
    if (value.loading || !value.action) return
    const signature = `${visitNumber}|${value.action}|${value.error}|${value.results.map((r) => `${r.session}:${r.ok}:${r.reason ?? ''}`).join(',')}`
    if (signature === reported) return
    reported = signature
    if (value.error) { host.error(`${roomControlLabels[value.action] ?? 'The action'} failed`, value.error); return }
    host.details(roomControlSummary(value, host.hierarchy()))
  }
  return {
    get value() { return value },
    get menu() { return menu },
    get confirm() { return confirm },
    visit, items, report, askClear,
    openMenu(x: number, y: number, anchor?: HTMLElement) { menu = { x, y, anchor, roomId: host.room(), visit: visitNumber } },
    closeMenu() { const anchor = menu?.anchor; menu = null; anchor?.focus?.() },
    dismiss() { confirm = null },
    clear(context: RoomContext) {
      if (!current(context) || confirm?.visit !== context.visit) return
      confirm = null
      controller?.run('clearDormant')
    },
    load() { controller?.load() },
    run(action: string) { controller?.run(action) },
    destroy() { alive = false; visitNumber++; controller?.destroy(); controller = null },
  }
}
