import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { domNames, installDom, load } from '../prs/prDetailHarness.ts'

type Computer = {
  session: string
  name: string
  state: 'running' | 'starting' | 'stopped' | 'none'
  reason?: string
  width?: number
  height?: number
  startedAt?: string
  shotAt?: string
}
type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
  unmount: (app: Mounted) => void
  flushSync: (fn?: () => void) => void
}
type View = { target: HTMLElement; opened: Computer[]; flushSync: Harness['flushSync'] }

// need returns the value, or throws when a test expects it to exist.
function need<T>(value: T | null | undefined, what: string): T {
  if (value === null || value === undefined) throw new Error(`missing ${what}`)
  return value
}
const one = <E extends Element = HTMLElement>(root: ParentNode, selector: string): E => need(root.querySelector<E>(selector), selector)
const textOf = (node: Node): string => need(node.textContent, 'text')

const loads: string[] = []

async function withView(list: Computer[], run: (view: View) => void | Promise<void>): Promise<void> {
  mock.timers.enable({ apis: ['setInterval', 'Date'], now: Date.parse('2026-01-01T01:00:00Z') })
  const dom = installDom()
  Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
  Object.assign(globalThis, { Image: class { set src(v: string) { loads.push(v) } } })
  const { dir, default: Computers, mount, unmount, flushSync }: Harness = await load({ component: '../computers/Computers', api: "export const api = { computerShotURL: (s, fresh) => `x.png${fresh ? '?fresh=1' : ''}` }" })
  const opened: Computer[] = []
  const target = need(document.getElementById('app'), 'app element')
  let app: Mounted | undefined
  try {
    flushSync(() => { app = mount(Computers, { target, props: { list, onopen: (c: Computer) => opened.push(c) } }) })
    await run({ target, opened, flushSync })
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
    Reflect.deleteProperty(globalThis, 'Image')
    loads.length = 0
    mock.timers.reset()
    await rm(dir, { recursive: true, force: true })
  }
}

const stopped: Computer = { session: 's1', name: 'api-payloads', state: 'stopped', reason: 'idle timeout with no connected apps' }
const toggle = (target: HTMLElement): HTMLButtonElement | undefined => [...target.querySelectorAll('button')].find((b) => /^(Show|Hide) stopped/.test(textOf(b).trim()))
const toggleText = (target: HTMLElement): string => textOf(need(toggle(target), 'stopped toggle')).trim()

test('with nothing running the view is calm and lists no stopped computers', async () => {
  await withView([stopped, { session: 's2', name: 'docs', state: 'none' }], ({ target }) => {
    assert.ok(textOf(target).includes('No computers running'))
    assert.ok(textOf(target).includes('An agent starts one'))
    assert.equal(target.querySelector('button.row'), null)
    assert.ok(!textOf(target).includes('api-payloads'))
    assert.equal(toggleText(target), 'Show stopped (2)')
  })
})

test('Show stopped lists them; a stopped row keeps its full status and opens the computer', async () => {
  await withView([stopped], ({ target, opened, flushSync }) => {
    flushSync(() => need(toggle(target), 'stopped toggle').click())
    assert.equal(toggleText(target), 'Hide stopped')
    const row = one<HTMLButtonElement>(target, 'button.row')
    assert.ok(textOf(row).includes('api-payloads'))
    assert.equal(row.querySelector<HTMLElement>('[title]')?.title, 'Stopped · idle timeout with no connected apps')
    row.click()
    assert.equal(need(opened[0], 'opened computer').reason, stopped.reason)
    flushSync(() => need(toggle(target), 'stopped toggle').click())
    assert.equal(target.querySelector('button.row'), null)
  })
})

test('with no computers at all there is no toggle', async () => {
  await withView([], ({ target }) => {
    assert.ok(textOf(target).includes('No computers running'))
    assert.equal(toggle(target), undefined)
  })
})

test('a running computer shows as a tile, with no empty state', async () => {
  await withView([{ session: 's3', name: 'feed', state: 'running', shotAt: '2026-01-01T00:00:00Z' }, stopped], ({ target }) => {
    assert.ok(!textOf(target).includes('No computers running'))
    assert.ok(target.querySelector<HTMLButtonElement>('button.g')?.textContent?.includes('feed'))
    assert.equal(toggleText(target), 'Show stopped (1)')
    assert.equal(target.querySelector('button.row'), null)
  })
})

const run = (extra?: Partial<Computer>): Computer => ({ width: 1280, height: 800, session: 's3', name: 'feed', state: 'running', startedAt: '2026-01-01T00:48:00Z', shotAt: '2026-01-01T00:59:50Z', ...extra })
const setVisible = (v: string): void => { Object.defineProperty(document, 'visibilityState', { value: v, configurable: true }) }

test('a running tile matches the aspect ratio of its computer and shows its name, status and uptime', async () => {
  await withView([run()], ({ target }) => {
    const tile = one<HTMLButtonElement>(target, 'button.g')
    assert.equal(one<HTMLElement>(tile, '.shot').style.aspectRatio, '1280 / 800', 'no letterbox bars')
    assert.ok(textOf(tile).includes('feed'))
    assert.ok(textOf(tile).includes('up 12 min'))
    const chip = one(tile, '.chip')
    assert.equal(textOf(chip).trim(), 'Running')
    assert.ok(chip.classList.contains('sage'), 'running is green')
    assert.equal(tile.querySelector('.chip .dot'), null)
  })
})

test('a starting tile shows an amber Starting chip', async () => {
  await withView([run({ state: 'starting', startedAt: undefined })], ({ target }) => {
    const chip = one(target, 'button.g .chip')
    assert.equal(textOf(chip).trim(), 'Starting…')
    assert.ok(chip.classList.contains('gold'))
  })
})

test('pictures refresh every 15 s while visible and stop while the page is hidden', async () => {
  await withView([run()], () => {
    const fresh = () => loads.filter((u) => u.includes('fresh')).length
    assert.equal(fresh(), 1, 'one fresh picture on first show')
    mock.timers.tick(15000)
    assert.equal(fresh(), 2)
    setVisible('hidden')
    mock.timers.tick(60000)
    assert.equal(fresh(), 2, 'no request while hidden')
    setVisible('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    assert.equal(fresh(), 3, 'a picture at once when the page comes back')
  })
})

test('the gallery has no stats footer and keeps two columns on a phone', async () => {
  await withView([run()], ({ target }) => {
    assert.ok(!/streams? open|RAM|screenshots, not streams/.test(textOf(target)))
    const src = readFileSync(new URL('./Computers.svelte', import.meta.url), 'utf8')
    assert.doesNotMatch(src, /max-width: 480px/)
    assert.match(src, /max-width: 760px\).*grid-template-columns: 1fr 1fr/)
  })
})
