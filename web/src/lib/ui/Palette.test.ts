import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom, FORMAT } from '../prs/prDetailHarness.ts'

const code = await bundle({
  component: '../ui/Palette',
  api: `export const api = {
    browseInfo: async () => ({ home: '/home/test' }),
    searchFeed: (...args) => { globalThis.searches.push(args); return globalThis.answer(...args) },
  }`,
  format: FORMAT + ` export const roomLabel = (r) => r.name; export const stamp = () => '12:00';`,
})

const post = (id: string, text: string) => ({ wake: false, id, ts: 1, roomId: 'room_x', from: 'repo:lead', to: ['repo'], text, owner: false, completion: false, fromHue: 2, speaker: 'lead' })
const targets = [{ kind: 'session', key: 'ses_deploy', label: 'deploy-fix', hint: 'working', terms: '', session: { id: 'ses_deploy', name: 'deploy-fix' } }]
function need<T>(value: T | null | undefined): T {
  assert.ok(value !== null && value !== undefined)
  return value
}
let flush = () => {}
const settle = async () => { for (let i = 0; i < 6; i++) { await Promise.resolve(); flush() } }

async function render(answer: (...args: unknown[]) => Promise<unknown>) {
  const searches: unknown[][] = []
  const dom = installDom({ searches, answer })
  Reflect.set(dom.window.Element.prototype, 'scrollIntoView', () => {})
  const { dir, default: Palette, mount, unmount, flushSync } = await importBundle(code, '../ui/Palette')
  flush = flushSync
  const jumps: string[] = []
  const opened: unknown[] = []
  let closed = 0
  const target = need(document.getElementById('app'))
  const component = mount(Palette, { target, props: { roomId: 'room_x', roomName: 'repo', targets, onmessage: (id: string) => jumps.push(id), ontarget: (t: unknown) => opened.push(t), onclose: () => closed++ } })
  await settle()
  const box = need(target.querySelector('input'))
  const type = (text: string) => { box.value = text; box.dispatchEvent(new Event('input', { bubbles: true })); flush() }
  const key = (key: string) => box.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  const done = async () => {
    await unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'searches', 'answer']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
  return { target, box, type, key, searches, jumps, opened, closed: () => closed, done }
}

test('one palette groups session and whole-room message matches, with keyboard and click jumps', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const r = await render(async () => ({ posts: [post('msg_old', 'The deploy failed outside the loaded feed')], more: true }))
  try {
    assert.equal(document.activeElement, r.box)
    r.type(' deploy ')
    t.mock.timers.tick(250)
    await settle()
    assert.deepEqual(r.searches, [['room_x', 'deploy']])
    assert.deepEqual([...r.target.querySelectorAll('.group')].map((n) => n.textContent?.trim()), ['Sessions', 'Messages · repo'])
    assert.equal(r.target.querySelector('mark')?.textContent, 'deploy')
    assert.match(r.target.textContent ?? '', /newest 1 matches/)
    r.key('ArrowDown')
    r.key('Enter')
    await settle()
    assert.deepEqual(r.jumps, ['msg_old'])
    assert.equal(r.closed(), 1)
    need(r.target.querySelector<HTMLButtonElement>('[role="option"]')).click()
    assert.deepEqual(r.opened, [targets[0]])
  } finally { await r.done() }
})

test('query changes hide old matches immediately and ignore late answers; failed searches can retry', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let resolveOld: (value: unknown) => void = () => {}
  let fail = true
  const r = await render(async (_room, query) => {
    if (query === 'old') return new Promise((resolve) => { resolveOld = resolve })
    if (query === 'failed' && fail) throw new Error('Search unavailable')
    return { posts: [post('msg_new', String(query))], more: false }
  })
  try {
    r.type('old')
    t.mock.timers.tick(250)
    await settle()
    r.type('fresh')
    resolveOld({ posts: [post('msg_old', 'old response')], more: false })
    await settle()
    assert.equal(r.target.querySelector('.message'), null)
    r.key('Enter') // Search now, without the typing pause.
    await settle()
    assert.equal(r.target.querySelector('.message .excerpt')?.textContent, 'fresh')
    r.type('failed')
    assert.equal(r.target.querySelector('.message'), null)
    r.key('Enter')
    await settle()
    assert.equal(r.target.querySelector('[role="alert"]')?.textContent, 'Search unavailable')
    fail = false
    need(r.target.querySelector<HTMLButtonElement>('.retry')).click()
    await settle()
    assert.equal(r.target.querySelector('.message .excerpt')?.textContent, 'failed')
    r.type('')
    await settle()
    assert.equal(r.target.querySelector('.message'), null)
  } finally { await r.done() }
})
