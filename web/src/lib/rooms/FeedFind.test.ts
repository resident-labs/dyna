import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom, FORMAT } from '../prs/prDetailHarness.ts'
import { snippet } from './feedFind.ts'

type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  flushSync: () => void
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
}

function need<T>(value: T | null | undefined, what: string): T {
  if (value === null || value === undefined) throw new Error(`missing ${what}`)
  return value
}

let flush = () => {}
const settle = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); flush() } }
const post = (id: string, text: string) => ({ wake: false, id, ts: 1, roomId: 'r', from: 'repo:lead', to: ['repo'], text, owner: false, completion: false, fromHue: 2, speaker: 'lead' })
// The stub answers from globalThis, so a test can change the answer.
const api = `export const api = {
  searchFeed: (...a) => { globalThis.searches.push(a); return globalThis.pending ?? Promise.resolve(globalThis.found) },
}`
const code = await bundle({ component: '../rooms/FeedFind', api, format: FORMAT + ' export const stamp = () => "12:00";' })

async function render(found: unknown) {
  const dom = installDom({ searches: [], found })
  // jsdom has no scrollIntoView; the test records which row asked for it.
  const scrolled: string[] = []
  Reflect.set(dom.window.Element.prototype, 'scrollIntoView', function (this: Element) { scrolled.push(this.id) })
  const { dir, default: FeedFind, mount, flushSync }: Harness = await importBundle(code, '../rooms/FeedFind')
  flush = flushSync
  const jumps: string[] = []
  let closed = 0
  const target = need(document.getElementById('app'), 'app element')
  mount(FeedFind, { target, props: { roomId: 'room_x', roomName: 'repo', onjump: (id: string) => jumps.push(id), onclose: () => closed++ } })
  await settle()
  const box = need(target.querySelector('input'), 'find box')
  const key = (k: string, shiftKey = false) => box.dispatchEvent(new KeyboardEvent('keydown', { key: k, shiftKey, bubbles: true }))
  const type = (text: string) => { box.value = text; box.dispatchEvent(new Event('input', { bubbles: true })) }
  const searches = (): unknown => Reflect.get(globalThis, 'searches')
  const done = async () => { dom.window.close(); for (const n of [...domNames, 'searches', 'found', 'pending']) Reflect.deleteProperty(globalThis, n); await rm(dir, { recursive: true, force: true }) }
  return { target, box, key, type, jumps, scrolled, searches, closed: () => closed, done }
}

test('find asks the server for the whole room, and each result jumps the feed to its post', async () => {
  const r = await render({ posts: [post('msg_new', 'the deploy is green'), post('msg_old', 'Deploy failed on CI'), post('msg_older', 'deploy notes')], more: true })
  try {
    assert.equal(document.activeElement, r.box, 'the box takes focus when the bar opens')
    r.type('  deploy ')
    r.key('Enter')
    await settle()
    assert.deepEqual(r.searches(), [['room_x', 'deploy']], 'Enter searches without waiting for the typing pause')
    const rows = [...r.target.querySelectorAll<HTMLButtonElement>('.row')]
    assert.equal(rows.length, 3)
    assert.equal(need(rows[1], 'row').querySelector('mark')?.textContent, 'Deploy')
    assert.match(r.target.textContent ?? '', /3\+ found/)
    r.key('Enter')
    r.key('Enter')
    r.key('Enter', true)
    need(rows[1], 'row').click()
    assert.deepEqual(r.jumps, ['msg_new', 'msg_old', 'msg_new', 'msg_old'])
    assert.deepEqual(r.searches(), [['room_x', 'deploy']], 'jumping does not search again')
    r.key('Escape')
    assert.equal(r.closed(), 1)
  } finally {
    await r.done()
  }
})

test('arrow keys move the highlight, scroll its row into view and name it as the active option', async () => {
  const r = await render({ posts: [post('a', 'x deploy'), post('b', 'y deploy'), post('c', 'z deploy')], more: false })
  try {
    r.type('deploy')
    r.key('Enter')
    await settle()
    r.key('ArrowDown')
    r.key('ArrowDown')
    await settle()
    assert.equal(r.box.getAttribute('aria-activedescendant'), 'find-row-2')
    assert.equal(r.target.querySelector('#find-row-2')?.getAttribute('aria-selected'), 'true')
    assert.equal(r.scrolled.at(-1), 'find-row-2')
    r.key('Enter')
    assert.deepEqual(r.jumps, ['c'])
  } finally {
    await r.done()
  }
})

test('Enter waits for a search in flight, and a failed search can be sent again', async () => {
  const r = await render({ posts: [post('old', 'deploy')], more: false })
  try {
    r.type('deploy')
    r.key('Enter')
    await settle()
    let answer = (_v: unknown) => {}
    Reflect.set(globalThis, 'pending', new Promise((resolve) => { answer = resolve }))
    r.type('other')
    r.key('Enter')
    r.key('Enter')
    assert.deepEqual(r.jumps, [], 'Enter does not jump within the older results')
    answer({ posts: null })
    await settle()
    assert.match(need(r.target.querySelector('[role=alert]'), 'error').textContent ?? '', /cannot read/)
    Reflect.set(globalThis, 'pending', undefined)
    Reflect.set(globalThis, 'found', { posts: [], more: false })
    r.key('Enter')
    await settle()
    assert.match(r.target.textContent ?? '', /Nothing in this room matches “other”/)
    r.type('')
    await settle()
    assert.equal(r.target.querySelector('[role=alert]'), null)
    assert.equal(r.target.querySelectorAll('.row').length, 0)
  } finally {
    await r.done()
  }
})

test('visible search controls guard stale results and retry the same query after failure', async () => {
  const r = await render({ posts: [post('a', 'deploy'), post('b', 'deploy')], more: true })
  const button = (label: string) => need([...r.target.querySelectorAll('button')].find(b => b.getAttribute('aria-label') === label), label)
  try {
    r.type('deploy')
    r.key('Enter')
    await settle()
    assert.match(r.target.textContent ?? '', /1 of 2\+ found/)
    button('Next match').click()
    await settle()
    assert.deepEqual(r.jumps, ['b'])
    button('Previous match').click()
    await settle()
    assert.deepEqual(r.jumps, ['b', 'a'])
    r.box.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true }))
    assert.deepEqual(r.jumps, ['b', 'a'])
    r.type('different')
    await settle()
    assert.equal(button('Next match').disabled, true)
    r.key('ArrowDown')
    assert.equal(r.box.getAttribute('aria-activedescendant'), null)
    Reflect.set(globalThis, 'found', { posts: null })
    r.key('Enter')
    await settle()
    const retry = need([...r.target.querySelectorAll('button')].find(b => b.textContent === 'Retry'), 'Retry')
    Reflect.set(globalThis, 'found', { posts: [], more: false })
    retry.click()
    await settle()
    assert.match(r.target.textContent ?? '', /Nothing in this room matches “different”/)
    assert.equal(button('Next match').disabled, true)
    assert.deepEqual(r.searches(), [['room_x', 'deploy'], ['room_x', 'different'], ['room_x', 'different']])
  } finally { await r.done() }
})

const join = (parts: { text: string; hit: boolean }[]) => parts.map((p) => (p.hit ? `[${p.text}]` : p.text)).join('')

test('a snippet marks every query word, ignoring case, on one line', () => {
  assert.equal(join(snippet('Deploy failed\n\non CI; retry the deploy', 'deploy ci')), '[Deploy] failed on [CI]; retry the [deploy]')
  assert.equal(join(snippet('no match here', 'absent')), 'no match here')
})

test('a long post is cut around its first match', () => {
  const text = 'a'.repeat(300) + ' needle ' + 'b'.repeat(300)
  const out = join(snippet(text, 'needle', 60))
  assert.match(out, /^….*\[needle\].*…$/)
  assert.ok(out.length <= 64, out)
  // A match near the start keeps the start.
  assert.match(join(snippet('needle ' + 'b'.repeat(300), 'needle', 60)), /^\[needle\] b+…$/)
  // A match near the end shows a full window that ends at the text end.
  const tail = join(snippet('b'.repeat(300) + ' needle', 'needle', 60))
  assert.match(tail, /^…b+ \[needle\]$/)
  assert.equal(tail.length, 1 + 60 + 2)
})

test('a snippet starts at most 20 characters before the match, so a short phone line still shows it', () => {
  const out = join(snippet('a'.repeat(300) + ' needle ' + 'b'.repeat(300), 'needle'))
  assert.equal(out.indexOf('[needle]'), 1 + 20)
})

test('a post that matched only on its sender shows from its start, unmarked', () => {
  const out = join(snippet('start of a long post ' + 'b'.repeat(300), 'lead'))
  assert.match(out, /^start of a long post b+…$/)
})

test('overlapping matches are all marked, and a length-changing lowercase gets no marks', () => {
  assert.equal(join(snippet('aaa', 'aa')), '[aaa]')
  assert.equal(join(snippet('İstanbul deploy', 'deploy')), 'İstanbul deploy')
})
