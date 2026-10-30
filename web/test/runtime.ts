import { JSDOM } from 'jsdom'
import type { RuntimeEnvironment, RuntimeHost } from '../src/lib/app/runtime.svelte.ts'

export function runtimeFixture(overrides: Partial<RuntimeHost> = {}) {
  const dom = new JSDOM()
  Object.assign(globalThis, { window: dom.window })
  const timers = new Map<number, { fn: () => void; ms: number }>()
  let serial = 0
  let unauthorized = () => {}
  const host: RuntimeHost = {
    narrowQuery: '(max-width: 760px)',
    needPair: () => false, unauthorized() {}, setHalt() {}, setNarrow() {}, setDataDir() {}, setHealth() {},
    setUpdate() {}, setRoute() {}, setEngine() {}, loadProfiles() {}, openFirst() {}, handle() {},
    workerMessage() {}, hashChanged() {}, openDetail() {}, tickRooms() {}, startData() {}, popped() {},
    openHookLog() {}, setVisible() {}, visible: () => true, started: () => true, refreshVisible() {},
    syncRoom: () => '', syncOperation: () => undefined, loadSync() {}, computerVisible: () => false,
    loadComputers() {}, refreshTransfers() {}, destroyData() {}, ...overrides,
  }
  const env: RuntimeEnvironment = {
    window, document: window.document, navigator: window.navigator,
    boot: async () => null, onUnauthorized: (fn) => { unauthorized = fn },
    interval: (fn, ms) => { const id = ++serial; timers.set(id, { fn, ms }); return id },
    clearInterval: (id) => { timers.delete(id) }, now: () => 0,
  }
  return { dom, host, env, timers, unauthorized: () => unauthorized() }
}
