import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { postHandlers } from '../ui/postSelect.ts'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

// One selected id per view: the bar of that post shows, and no other.
// The posts live in installDom's page; the test reads them through the DOM types.
function view() {
  const dom = installDom()
  // installDom does not set FocusEvent; the test dispatches it.
  Reflect.set(globalThis, 'FocusEvent', Reflect.get(dom.window, 'FocusEvent'))
  const page = globalThis.document
  page.body.innerHTML = '<article data-select-id="a"><button>Reply</button><section data-select-id="n"></section><section data-select-id="n1"><button>Pin</button></section></article><article data-select-id="b"></article>'
  let selected = ''
  const selection = { get: () => selected, set: (id: string) => { selected = id } }
  const posts = new Map<string, Element>()
  for (const el of page.querySelectorAll('[data-select-id]')) {
    const id = el.getAttribute('data-select-id') ?? ''
    const on = postHandlers(selection, id)
    el.addEventListener('pointerdown', (e: Event) => on.onpointerdown(e))
    el.addEventListener('pointerenter', (e: Event) => on.onpointerenter(e))
    el.addEventListener('pointerleave', (e: Event) => on.onpointerleave(e))
    el.addEventListener('click', (e: Event) => { if (e instanceof MouseEvent) on.onclick(e) })
    el.addEventListener('focusin', (e: Event) => { if (e instanceof FocusEvent) on.onfocusin(e) })
    posts.set(id, el)
  }
  const at = (id: string): Element => {
    const el = posts.get(id)
    assert.ok(el, id)
    return el
  }
  // jsdom has no PointerEvent, so a plain event carries the pointer type.
  const pointer = (id: string, type: string, pointerType: string) => at(id).dispatchEvent(Object.assign(new Event(type, { bubbles: type === 'pointerdown' }), { pointerType }))
  const tap = (id: string, el: Element = at(id)) => {
    el.dispatchEvent(Object.assign(new Event('pointerdown', { bubbles: true }), { pointerType: 'touch' }))
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }
  const click = (id: string) => at(id).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  // Tab: a key goes down, then focus lands on el.
  const tab = (el: Element) => {
    page.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
    el.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  }
  const close = () => { dom.window.close(); for (const name of [...domNames, 'FocusEvent']) Reflect.deleteProperty(globalThis, name) }
  return { selected: () => selected, pointer, tap, click, tab, at, close }
}

test('one post is selected at a time, by tap or by mouse hover', () => {
  const v = view()
  v.tap('a')
  assert.equal(v.selected(), 'a')
  v.tap('b')
  assert.equal(v.selected(), 'b', 'a tap moves the selection; the old bar hides')
  v.tap('b')
  assert.equal(v.selected(), '', 'a second tap clears it')
  v.pointer('a', 'pointerenter', 'mouse')
  assert.equal(v.selected(), 'a')
  v.pointer('a', 'pointerleave', 'mouse')
  assert.equal(v.selected(), '')
  // A tap's synthetic hover never selects; only the tap does.
  v.pointer('b', 'pointerenter', 'touch')
  assert.equal(v.selected(), '')
  v.close()
})

test('a mouse click does not undo its hover, and controls and nested parts keep their taps', () => {
  const v = view()
  v.pointer('a', 'pointerenter', 'mouse')
  v.pointer('a', 'pointerdown', 'mouse')
  v.click('a')
  assert.equal(v.selected(), 'a')
  v.tap('a', v.at('a').querySelector('button') ?? v.at('a'))
  assert.equal(v.selected(), 'a', 'a button tap acts on its own')
  v.tap('n')
  assert.equal(v.selected(), 'n', 'a tap on a PR notice selects the notice, not its post')
  v.close()
})

// PRCard with the real component: one bar per notice, shown only for the selected notice.
const FORMAT = "export const markdown = (t) => t || ''; export const stamp = () => '08:56'; export const clock = () => '08:56'; export const short = (a) => a"
const built = await bundle({ component: 'PRCard', format: FORMAT, api: 'export const api = {}; export const openExternal = () => {}; export const copyText = () => Promise.resolve()' })
const notice = (id: string, n: number) => ({ id, kind: 'pr', from: 'pr:watch', to: ['work:voice'], ts: '2026-10-20T08:56:00Z', text: `https://github.com/o/r/pull/${n}\nHead: 0366830\n\nPR merged. Watching stopped.` })

test('Tab selects the nearest selectable part: an earlier grouped notice, not its post', () => {
  const v = view()
  v.tab(v.at('n1').querySelector('button') ?? v.at('n1'))
  assert.equal(v.selected(), 'n1', 'focus on a notice button selects that notice')
  v.tab(v.at('a'))
  assert.equal(v.selected(), 'a')
  // Focus after a tap does not select; only the tap does.
  v.pointer('b', 'pointerdown', 'touch')
  v.at('b').dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  assert.equal(v.selected(), 'a')
  v.close()
})

test('a PR post has one bar per notice and shows only the selected one', async () => {
  const dom = installDom()
  // The post's markdown and media parts read DOM classes installDom does not set.
  const extra = Object.getOwnPropertyNames(dom.window).filter((name) => /^(HTML|SVG|DOM|Node|Range)/.test(name) && !(name in globalThis))
  for (const name of extra) Reflect.set(globalThis, name, Reflect.get(dom.window, name))
  const { dir, default: PRCard, mount, flushSync } = await importBundle(built, 'PRCard')
  try {
    const target = dom.window.document.getElementById('app')
    assert.ok(target)
    const posts = [notice('n1', 1258), notice('n2', 1258)]
    const show = (selected: string): string[] => {
      target.replaceChildren()
      mount(PRCard, { target, props: { post: posts[1], posts, room: { id: 'r', name: 'work' }, selection: { get: () => selected, set: () => {} }, onreply: () => {}, onforward: () => {}, onpin: () => {} } })
      flushSync()
      assert.equal(target.querySelectorAll('.actions').length, 2, 'one bar per notice, none for the post')
      assert.ok([...target.querySelectorAll('[data-notice-id]')].every((n) => n.getAttribute('tabindex') === '0'), 'every notice is in the Tab order')
      return [...target.querySelectorAll('.actions.show')].map((bar) => bar.closest('[data-notice-id]')?.getAttribute('data-notice-id') ?? '?')
    }
    assert.deepEqual(show(''), [])
    assert.deepEqual(show('n2'), ['n2'])
  } finally {
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})

// DmRows with two real message rows: they share one selection.
const dmBuilt = await bundle({ component: '../sessions/DmRows', format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8'), api: 'export const api = {}; export const openExternal = () => {}; export const copyText = () => Promise.resolve()' })
const message = (id: string) => ({ kind: 'message', key: `post:${id}`, ts: '2026-10-20T08:56:00Z', author: 'work:lead', text: id, post: { id, kind: 'message', from: 'work:lead', to: ['work'], ts: '2026-10-20T08:56:00Z', text: id } })

test('tapping DM message A and then B leaves only B with a bar', async () => {
  const dom = installDom()
  const extra = Object.getOwnPropertyNames(dom.window).filter((name) => /^(HTML|SVG|DOM|Node|Range)/.test(name) && !(name in globalThis))
  for (const name of extra) Reflect.set(globalThis, name, Reflect.get(dom.window, name))
  // Markdown parses HTML with DOMParser; jsdom's must be built from its window.
  const Parser = Reflect.get(dom.window, 'DOMParser')
  Reflect.set(globalThis, 'DOMParser', class {
    parseFromString(text: string, type: string): unknown {
      const parser: unknown = Reflect.construct(Parser, [])
      return typeof parser === 'object' && parser !== null ? Reflect.apply(Reflect.get(parser, 'parseFromString'), parser, [text, type]) : null
    }
  })
  const { dir, default: DmRows, mount, flushSync } = await importBundle(dmBuilt, 'DmRows')
  try {
    const target = dom.window.document.getElementById('app')
    assert.ok(target)
    const dm = { address: 'work:lead', threads: [], rows: [message('A'), message('B')] }
    mount(DmRows, { target, props: { dm, room: { id: 'r', name: 'work' }, member: { name: 'lead', address: 'work:lead' }, members: [], onforward: () => {}, onthread: () => {} } })
    flushSync()
    const article = (id: string): Element => {
      const el = target.querySelector(`article[data-select-id="${id}"]`)
      assert.ok(el, id)
      return el
    }
    const tapRow = (id: string) => {
      article(id).dispatchEvent(Object.assign(new Event('pointerdown', { bubbles: true }), { pointerType: 'touch' }))
      article(id).dispatchEvent(new MouseEvent('click', { bubbles: true }))
      flushSync()
    }
    const shown = () => [...target.querySelectorAll('.actions.show')].map((bar) => bar.closest('[data-select-id]')?.getAttribute('data-select-id'))
    tapRow('A')
    assert.deepEqual(shown(), ['A'])
    tapRow('B')
    assert.deepEqual(shown(), ['B'])
  } finally {
    dom.window.close()
    for (const name of [...domNames, ...extra]) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})

test('the feed and DMs draw no post bar for a PR post, and the bar has no CSS hover or focus rules', () => {
  const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
  assert.match(read('./Feed.svelte'), /p\.kind !== 'pr' && !sealedEvent\(p\)\}<PostActions/)
  // A registered event owns its own card and bar, in the feed and in a DM.
  assert.match(read('../sessions/DmRows.svelte'), /<Card post=\{row\.post\} notice=\{row\.post\.notice\} surface="dm" \{room\} onissue=/);
  // A session renders one DmRows per row and a thread of DmMessages; each list shares one selection.
  const session = read('../sessions/Session.svelte')
  assert.match(session, /<DmRows [^>]*selection=\{listSelection\}/)
  assert.match(session, /<DmMessage [^>]*selection=\{threadSelection\}/)
  const css = read('../ui/PostActions.svelte')
  assert.doesNotMatch(css.slice(css.indexOf('<style>')), /:hover\)? \.actions|focus-within|\.tapped/)
})
