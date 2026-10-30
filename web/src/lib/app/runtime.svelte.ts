import { api, engineCheck, onEvent, onUnauthorized, openExternal } from '../../api.ts'
import { browserEngine, engineWatch } from '../engineStatus.ts'
import { boot, registerWorker, watch } from '../settings/link.ts'
import { HOOK_LOG_EVENT, hookLogFrom } from '../settings/hookLog.ts'
import { parseDetailHref } from '../ui/details.ts'
import { followInternalLink, internalLinkHash } from '../ui/format.ts'
import { syncDelay } from '../lanesync/sync.ts'
import type { EngineStatus } from '../engineStatus.ts'
import type { BootResult, RouteNotice } from '../settings/link.ts'
import type { StreamEvent } from '../api/event.ts'
import type { UpdateNotice } from '../api/settings.ts'
import type { HookLogTarget } from '../settings/hookLog.ts'

export interface RuntimeHost {
  narrowQuery: string
  needPair(): boolean
  unauthorized(): void
  setHalt(value: BootResult): void
  setNarrow(value: boolean): void
  setDataDir(value: string): void
  setHealth(value: string): void
  setUpdate(value: UpdateNotice | null): void
  setRoute(value: RouteNotice): void
  setEngine(value: EngineStatus): void
  loadProfiles(): void
  openFirst(): void
  handle(event: StreamEvent): void
  workerMessage(event: MessageEvent): void
  hashChanged(): void
  openDetail(target: NonNullable<ReturnType<typeof parseDetailHref>>): void
  tickRooms(): void
  startData(): void
  popped(): void
  openHookLog(target: HookLogTarget): void
  setVisible(value: boolean): void
  visible(): boolean
  started(): boolean
  refreshVisible(): void
  syncRoom(): string
  syncOperation(): Parameters<typeof syncDelay>[0]
  loadSync(): void
  computerVisible(): boolean
  loadComputers(): void
  refreshTransfers(): void
  destroyData(): void
}

export interface RuntimeEnvironment {
  window: Window
  document: Document
  navigator: Navigator
  boot: typeof boot
  onUnauthorized: typeof onUnauthorized
  interval(fn: () => void, ms: number): number
  clearInterval(id: number): void
  now(): number
  // Tests can supply a fake connected event source.
  connect?: () => () => void
}

export function createRuntime(host: RuntimeHost, env: RuntimeEnvironment = {
  window, document, navigator, boot, onUnauthorized, interval: (fn, ms) => window.setInterval(fn, ms), clearInterval: (id) => window.clearInterval(id), now: Date.now,
}) {
  let gone = false
  let stopApp = () => {}
  let stop = () => {}
  function startConnected() {
    const mq = env.window.matchMedia(host.narrowQuery)
    const resize = () => host.setNarrow(mq.matches)
    mq.addEventListener('change', resize)
    api.dataDir().then(host.setDataDir)
    api.health().then((h) => host.setHealth(h?.status ?? ''))
    host.loadProfiles()
    api.updateNotice().then((n) => host.setUpdate(n?.text ? n : null)).catch(() => {})
    host.openFirst()
    const link = watch(host.setRoute)
    const engine = engineWatch(host.setEngine, browserEngine(engineCheck))
    const off = (onEvent as (event: (ev: StreamEvent) => void, state: (up: boolean) => void) => () => void)(host.handle, (up) => { link.streamState(up); engine.state(up) })
    registerWorker()
    env.navigator.serviceWorker?.addEventListener('message', host.workerMessage)
    env.window.addEventListener('hashchange', host.hashChanged)
    const clicks = (e: MouseEvent) => {
      const a = e.target instanceof Element ? e.target.closest<HTMLAnchorElement>('a[href]') : null
      if (!a) return
      e.preventDefault()
      const target = parseDetailHref(a.getAttribute('href'))
      if (target) { host.openDetail(target); return }
      const internal = internalLinkHash(a.href, location)
      if (internal !== null) { followInternalLink(internal, location); return }
      if (/^https?:/.test(a.href)) openExternal(a.href)
    }
    env.document.addEventListener('click', clicks)
    const ticker = env.interval(host.tickRooms, 30000)
    host.startData()
    return () => {
      mq.removeEventListener('change', resize)
      off()
      link.stop()
      engine.stop()
      env.navigator.serviceWorker?.removeEventListener('message', host.workerMessage)
      env.window.removeEventListener('hashchange', host.hashChanged)
      env.document.removeEventListener('click', clicks)
      env.clearInterval(ticker)
    }
  }
  function start() {
    const hookLog = (event: Event) => {
      const target = hookLogFrom(event)
      if (target) host.openHookLog(target)
    }
    const visibility = () => {
      host.setVisible(env.document.visibilityState === 'visible')
      if (host.visible() && host.started()) host.refreshVisible()
    }
    env.window.addEventListener('dyna:computer-popped', host.popped)
    env.window.addEventListener(HOOK_LOG_EVENT, hookLog)
    env.document.addEventListener('visibilitychange', visibility)
    let syncAt = 0
    const syncPoll = env.interval(() => {
      if (host.needPair() || !host.visible() || !host.syncRoom() || env.now() - syncAt < syncDelay(host.syncOperation())) return
      syncAt = env.now()
      host.loadSync()
    }, 1000)
    const computerPoll = env.interval(() => { if (!host.needPair() && host.visible() && host.computerVisible()) host.loadComputers() }, 10000)
    if (!host.needPair()) env.boot().then((h) => {
      if (gone || host.needPair()) return
      if (h) host.setHalt(h)
      else stopApp = env.connect ? env.connect() : startConnected()
    })
    const transferPoll = env.interval(() => {
      if (host.needPair() || !host.visible()) return
      host.refreshTransfers()
    }, 5000)
    function destroy() {
      if (gone) return
      gone = true
      env.window.removeEventListener('dyna:computer-popped', host.popped)
      env.window.removeEventListener(HOOK_LOG_EVENT, hookLog)
      env.document.removeEventListener('visibilitychange', visibility)
      env.clearInterval(computerPoll)
      env.clearInterval(syncPoll)
      host.destroyData()
      env.clearInterval(transferPoll)
      stopApp()
    }
    stop = destroy
    env.onUnauthorized(() => { if (gone) return; host.unauthorized(); destroy() })
    return destroy
  }
  return { start, destroy: () => stop() }
}
