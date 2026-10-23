import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { domNames, installDom } from '../prs/prDetailHarness.ts'
import { compileSvelte, svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

function deferred<T = boolean>() {
  let resolve: (value: T) => void = () => { throw new Error('the deferred value is not ready') }
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
const rows = (start: number, count: number) => Array.from({ length: count }, (_, i) => ({ id: `p${start + i}` }))

async function harness(thread = false) {
  const dom = installDom()
  Reflect.set(globalThis, 'matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  const observed = new Set<Element>()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  globalThis.DOMParser = dom.window.DOMParser
  globalThis.NodeFilter = dom.window.NodeFilter
  interface SizeEntry { target: Element; borderBoxSize: { inlineSize: number; blockSize: number }[] }
  Reflect.set(globalThis, 'ResizeObserver', class {
    cb?: (entries: SizeEntry[]) => void
    observe(el: Element) { observed.add(el); this.cb?.([{ target: el, borderBoxSize: [{ inlineSize: 800, blockSize: (el as HTMLElement).offsetHeight }] }]) }
    unobserve(el: Element) { observed.delete(el) }
    disconnect() {}
    constructor(cb: (entries: SizeEntry[]) => void) { this.cb = cb }
  })
  dom.window.ResizeObserver = globalThis.ResizeObserver
  const proto = dom.window.HTMLElement.prototype
  for (const name of ['offsetHeight', 'clientHeight']) Object.defineProperty(proto, name, { configurable: true, get() { return (this.id === 'app' || this.classList.contains('thread-log') || this.classList.contains('log')) ? 500 : this.classList.contains('vlist') ? parseFloat(this.style.height) || 0 : 40 } })
  for (const name of ['offsetWidth', 'clientWidth']) Object.defineProperty(proto, name, { configurable: true, get: () => 800 })
  Object.defineProperty(proto, 'offsetTop', { configurable: true, get() { return this.classList.contains('vlist') ? 36 : 0 } })
  Object.defineProperty(proto, 'scrollHeight', { configurable: true, get() { if (this.classList.contains('log')) return 2000; return (this.id === 'app' || this.classList.contains('thread-log')) ? 36 + (parseFloat(this.querySelector('.vlist')?.style.height) || 0) : this.offsetHeight } })
  Reflect.set(proto, 'scrollTo', function(this: HTMLElement, options?: ScrollToOptions) { const top = options?.top ?? 0; this.scrollTop = top; queueMicrotask(() => this.dispatchEvent(new Event('scroll'))) })
  proto.getBoundingClientRect = function() {
    const y = this.classList.contains('vrow') ? 36 + Number(this.style.transform.match(/translateY\(([-.\d]+)px\)/)?.[1] || 0) - (document.getElementById('app')?.scrollTop ?? 0) : 0
    return { x: 0, y, top: y, left: 0, width: 800, height: this.offsetHeight, bottom: y + this.offsetHeight, right: 800 } as DOMRect
  }
  const dir = await mkdtemp(join(tmpdir(), 'dyna-history-test-'))
  const direct = `<script>
    import HistoryList from '${new URL('./HistoryList.svelte', import.meta.url).pathname}'
    let { model, load } = $props()
    let list
    export const scrollToIndex = (i) => list.scrollToIndex(i, 'start')
  </script>
  {#key $model.room}
  <HistoryList bind:this={list} scroller={$model.scroller} items={$model.items} key={(p) => p.id} estimate={() => 40} earlier={$model.earlier} newer={$model.newer} onnewer={$model.onnewer} onpresent={$model.onpresent} onearlier={load} beginning="Beginning of #general">
    {#snippet children(row)}<div data-post={row.id}>{row.id}</div>{/snippet}
  </HistoryList>
  {/key}`
  const wrapper = !thread ? direct : `<script>
    import Session from '${new URL('../sessions/Session.svelte', import.meta.url).pathname}'
    let { model, load } = $props()
    let session = $derived({
      member: { id: 'lead', name: 'lead', address: 'alpha:lead', state: 'idle', turns: 0 },
      turns: [], earlier: $model.earlier, newer: $model.newer,
      posts: $model.items.map((p, i) => ({ id: p.id, from: 'alpha:peer', to: ['alpha:lead'], ts: 1700000000000 + i * 1000, text: p.id })),
    })
  </script>
  <Session {session} room={{ id: 'room', name: 'alpha' }} threadsOpen selectedThread="alpha:peer" onearlier={load} onnewer={$model.onnewer} onpresent={$model.onpresent} />`
  const compiled = await build({
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser'], logLevel: 'silent',
    stdin: { contents: `export { mount, flushSync, tick, unmount } from 'svelte'; export { writable } from 'svelte/store'; export { default } from 'wrapper.svelte'`, resolveDir: new URL('.', import.meta.url).pathname },
    plugins: [svelteEsbuildPlugin(), { name: 'wrapper', setup(b) {
      b.onResolve({ filter: /^wrapper\.svelte$/ }, () => ({ path: 'wrapper.svelte', namespace: 'wrapper' }))
      b.onLoad({ filter: /.*/, namespace: 'wrapper' }, () => ({ contents: compileSvelte(wrapper, 'wrapper.svelte'), resolveDir: new URL('.', import.meta.url).pathname }))
    } }],
  })
  const built = compiled.outputFiles?.[0]
  assert.ok(built, 'the history list built')
  const file = join(dir, 'history.mjs'); await writeFile(file, built.contents)
  const api = await import(pathToFileURL(file).href)
  const target = document.getElementById('app')
  assert.ok(target)
  let state: { scroller: HTMLElement; items: { id: string }[]; earlier: boolean; room?: string; newer?: boolean; onnewer?: () => void; onpresent?: () => void } = { scroller: target, items: rows(10, 50), earlier: true }
  const model = api.writable(state)
  let component: object | undefined
  const update = (patch: Partial<typeof state>) => { state = { ...state, ...patch }; model.set(state); api.flushSync() }
  const scroll = (top: number) => { const el = thread ? target.querySelector<HTMLElement>('.thread-log') : target; assert.ok(el); el.scrollTop = top; el.dispatchEvent(new Event('scroll')); api.flushSync() }
  return { ...api, target, update, scroll, observed,
    until: (check: () => boolean) => new Promise<void>(resolve => {
      if (check()) return resolve()
      const observer = new MutationObserver(() => { if (check()) { observer.disconnect(); resolve() } })
      observer.observe(target, { childList: true, subtree: true, characterData: true })
    }),
    mount: (load: () => Promise<boolean>) => { component = api.mount(api.default, { target, props: { model, load } }); api.flushSync(); return component },
    close: async () => { if (component) await api.unmount(component); dom.window.close(); for (const n of [...domNames, 'DOMParser', 'NodeFilter', 'HTMLMediaElement', 'matchMedia']) Reflect.deleteProperty(globalThis, n); await rm(dir, { recursive: true, force: true }) },
  }
}

for (const thread of [false]) test((thread ? 'a thread' : 'a channel') + ' opens at its latest post on first load and after a switch', { timeout: 9000 }, async () => {
  const h = await harness(thread)
  const el = () => thread ? h.target.querySelector('.thread-log') : h.target
  const gap = () => el().scrollHeight - el().clientHeight - el().scrollTop
  try {
    h.mount(async () => false)
    await h.tick()
    assert.ok(gap() < 48, `first load is ${gap()}px above the latest post`)
    assert.ok(h.target.querySelector('[data-post="p59"], [data-key="p59"]') || h.target.textContent.includes('p59'), 'the latest post renders')
    if (thread) return
    h.scroll(0)
    h.update({ room: 'other', items: rows(200, 50) })
    await h.tick()
    assert.ok(gap() < 48, `a switched channel is ${gap()}px above the latest post`)
    assert.ok(h.target.querySelector('[data-post="p249"]'), 'the latest post renders')
  } finally { await h.close() }
})

test('a jump right after a channel switch wins over opening at the latest post', { timeout: 9000 }, async () => {
  const h = await harness()
  try {
    const feed = h.mount(async () => false)
    h.update({ room: 'other', items: rows(200, 50) })
    feed.scrollToIndex(5)
    await h.tick()
    // Row 5 starts 5 rows below the 36px history marker.
    assert.equal(h.target.scrollTop, 36 + 5 * 40)
  } finally { await h.close() }
})

test('a reader above the latest post keeps their place when new posts arrive', { timeout: 9000 }, async () => {
  const h = await harness()
  try {
    h.mount(async () => false)
    await h.tick()
    h.scroll(600)
    h.update({ items: rows(10, 52) })
    await h.tick()
    assert.equal(h.target.scrollTop, 600)
  } finally { await h.close() }
})

test('older page keeps the visible post at the same pixel when the beginning marker replaces loading', { timeout: 9000 }, async () => {
  const h = await harness(), page = deferred()
  try {
    h.mount(() => page.promise)
    h.scroll(600); h.scroll(240)
    await h.tick()
    const post = h.target.querySelector('[data-post="p16"]')
    assert.ok(post, 'anchor post is rendered')
    const before = post.parentElement.getBoundingClientRect().top
    h.update({ items: rows(0, 60), earlier: false })
    page.resolve(true); await h.until(() => h.target.textContent.includes('Beginning of #general')); h.flushSync()
    assert.equal(h.target.querySelector('[data-post="p16"]').parentElement.getBoundingClientRect().top, before)
    assert.match(h.target.textContent, /Beginning of #general/)
  } finally { await h.close() }
})

test('prefetch starts within one viewport and a second scroll shares the page in flight', { timeout: 9000 }, async () => {
  const h = await harness(), page = deferred(); let calls = 0
  try {
    h.mount(() => { calls++; return page.promise })
    h.scroll(700); h.scroll(350); await h.tick()
    assert.equal(calls, 1)
    assert.ok(h.target.querySelector('[aria-label="Loading earlier messages"]'))
    h.scroll(100); h.scroll(50); await h.tick()
    assert.equal(calls, 1)
    h.update({ earlier: false }); page.resolve(true); await h.tick()
  } finally { await h.close() }
})

test('beginning marker appears only after older pages end', { timeout: 9000 }, async () => {
  const h = await harness()
  try {
    h.mount(async () => false)
    assert.doesNotMatch(h.target.textContent, /Beginning of/)
    h.update({ earlier: false })
    assert.match(h.target.textContent, /Beginning of #general/)
    assert.equal(h.target.querySelectorAll('.vrow').length < 50, true, 'list remains virtualized')
  } finally { await h.close() }
})

for (const thread of [false, true]) test((thread ? 'selected thread' : 'channel') + ' continues past an empty filtered page at the top of a full viewport', { timeout: 9000 }, async () => {
  const h = await harness(thread), empty = deferred()
  let calls = 0
  try {
    h.mount(() => {
      calls++
      if (calls === 1) return empty.promise
      h.update({ items: rows(0, 60), earlier: false })
      return Promise.resolve(true)
    })
    h.scroll(700); h.scroll(350)
    await h.tick()
    assert.equal(calls, 1)
    h.scroll(0)
    empty.resolve(true)
    await new Promise(setImmediate)
    h.flushSync()
    assert.equal(calls, 2, 'the top cannot scroll farther upward to request the next visible page')
  } finally { await h.close() }
})

test('an empty filtered page stops prefetching when the reader leaves the top', { timeout: 9000 }, async () => {
  const h = await harness(), page = deferred()
  let calls = 0
  try {
    h.mount(() => { calls++; return page.promise })
    h.scroll(700); h.scroll(350)
    await h.tick()
    h.scroll(1000)
    page.resolve(true)
    await new Promise(setImmediate)
    assert.equal(calls, 1)
  } finally { await h.close() }
})

test('Jump to present reloads the tail and returns the shared list to the end', {timeout:9000}, async () => {
 const h=await harness(); let calls=0
 try {
  h.update({newer:true,onpresent:async()=>{calls++;h.update({items:rows(100,50),earlier:true,newer:false});return true}})
  h.mount(async()=>false)
  await h.tick()
  const jump=h.target.querySelector('[aria-label="Jump to present"]')
  assert.ok(jump)
  jump.click()
  await h.until(()=>!h.target.querySelector('[aria-label="Jump to present"]'))
  assert.equal(calls,1)
  assert.ok(h.target.querySelector('[data-post="p149"]'))
 } finally {await h.close()}
})

test('newer and older prefetch share one request in flight', {timeout:9000}, async () => {
 const h=await harness(), page=deferred();let older=0,newer=0
 try {
  h.update({newer:true,onnewer:()=>{newer++;return page.promise}})
  h.mount(()=>{older++;return Promise.resolve(true)})
  h.scroll(600);h.scroll(1600);await h.tick()
  assert.equal(newer,1)
  h.scroll(300);await h.tick()
  assert.equal(older,0)
  h.update({newer:false});page.resolve(true);await h.tick()
 } finally {await h.close()}
})

test('refetching newer rows at the old window bottom preserves the visible anchor', {timeout:9000}, async () => {
 const h=await harness(),page=deferred()
 try {
  h.update({newer:true,onnewer:()=>page.promise})
  h.mount(async()=>false)
  h.scroll(600);h.scroll(1536);await h.tick()
  const row=h.target.querySelector('[data-post="p55"]')
  assert.ok(row)
  const before=row.parentElement.getBoundingClientRect().top
  h.update({items:rows(10,100),newer:false})
  await h.tick()
  assert.equal(h.target.querySelector('[data-post="p55"]').parentElement.getBoundingClientRect().top,before)
  page.resolve(true);await h.tick()
 } finally {await h.close()}
})

test('an empty filtered history stops at the beginning instead of paging back and forth', {timeout:9000}, async () => {
 const h=await harness();let older=0,newer=0
 try {
  h.update({items:[],onnewer:async()=>{newer++;return false}})
  h.mount(async()=>{older++;h.update({earlier:false,newer:true});return true})
  await h.tick();await h.tick()
  assert.equal(older,1)
  assert.equal(newer,0)
 } finally {await h.close()}
})

test('a replaced first date divider cannot take the visible post anchor', {timeout:9000}, async () => {
 const h=await harness(),page=deferred()
 try {
  h.update({items:[{id:'old-day'},...rows(10,50)]})
  h.mount(()=>page.promise)
  h.scroll(300);h.scroll(0);await h.tick()
  const before=h.target.querySelector('[data-post="p10"]').parentElement.getBoundingClientRect().top
  h.update({items:[{id:'new-day'},...rows(0,60)],earlier:false})
  page.resolve(true)
  await h.until(()=>h.target.textContent.includes('Beginning of #general'));h.flushSync()
  const post=h.target.querySelector('[data-post="p10"]')
  assert.ok(post,'the old visible post remains rendered')
  assert.equal(post.parentElement.getBoundingClientRect().top,before)
 } finally {await h.close()}
})

test('a wheel gesture at the loaded top can fetch the next page without a scroll offset change', {timeout:9000}, async () => {
 const h=await harness(),page=deferred();let calls=0
 try {
  h.mount(()=>{calls++;return page.promise})
  h.target.scrollTop=0
  h.target.dispatchEvent(new h.target.ownerDocument.defaultView.WheelEvent('wheel',{deltaY:-100}))
  await h.tick()
  assert.equal(calls,1)
  h.update({earlier:false});page.resolve(true);await h.tick()
 } finally {await h.close()}
})

test('scroll events queued before unmount cannot load history afterward', {timeout:9000}, async () => {
 const h=await harness();let calls=0
 h.mount(async()=>{calls++;return false})
 h.scroll(700);await h.tick()
 const target=h.target,event=new target.ownerDocument.defaultView.Event('scroll')
 await h.close()
 target.scrollTop=0
 target.dispatchEvent(event)
 await Promise.resolve()
 assert.equal(calls,0)
})


test('discarded history rows release their measured DOM nodes', { timeout: 9000 }, async () => {
 const h = await harness()
 try {
  h.mount(() => Promise.resolve(false))
  assert.ok([...h.observed].some(node => node.classList.contains('vrow')))
  for (let n=0;n<4;n++) {
   h.update({items:rows(100+n*50,50),earlier:false})
   await h.tick()
   assert.equal([...h.observed].filter(node => node.classList.contains('vrow') && !node.isConnected).length,0)
  }
 } finally { await h.close() }
})

test('a short channel list fills its viewport from older history; a short thread panel does not page the DM', { timeout: 9000 }, async () => {
  for (const thread of [false, true]) {
    const h = await harness(thread)
    let calls = 0
    try {
      h.update({ items: rows(0, 2), earlier: true })
      h.mount(async () => { calls++; return false })
      await h.tick()
      await new Promise(setImmediate)
      h.flushSync()
      if (thread) assert.equal(calls, 0, 'opening a short thread must not pull pages of the shared DM history')
      else assert.ok(calls >= 1, 'the main list still fills its viewport')
    } finally { await h.close() }
  }
})

// A reader who opens another page while a page request is in flight leaves the
// list behind. The request must not come back to a list it no longer owns: that
// read is the svelte derived_inert warning on Questions, Runs, Code, Terminal,
// Visuals and Computers (msg_2a65d81c38c84a8d).
test('a page request that outlives its view asks for nothing more', { timeout: 9000 }, async () => {
  const h = await harness(), page = deferred()
  let calls = 0
  const load = () => {
    calls++
    if (calls === 2) {
      // The posts move under the request, so only leaving stops the loop.
      h.update({ items: rows(-5, 5) })
      return page.promise
    }
    h.update({ items: rows(0, 5) })
    return Promise.resolve(true)
  }
  // The request chain runs on promises, so turn the microtask queue until it
  // reaches the wanted state; no timer and no sleep.
  const settle = async (check: () => boolean) => {
    for (let i = 0; i < 20 && !check(); i++) {
      await new Promise(setImmediate)
      h.flushSync()
    }
    return check()
  }
  try {
    const list = h.mount(load)
    h.scroll(600); h.scroll(240)
    assert.ok(await settle(() => calls >= 2), `the first page asks for the next one (calls=${calls})`)
    await h.unmount(list)
    page.resolve(true)
    await settle(() => false)
    assert.equal(calls, 2, 'the destroyed list asks for nothing more')
  } finally { await h.close() }
})

test('a local jump appears half a viewport from the end and does not reload history', async () => {
  const h = await harness()
  let calls = 0
  try {
    h.update({ onpresent: async () => { calls++; return true } })
    h.mount(async () => false)
    await h.tick()
    h.scroll(h.target.scrollHeight - h.target.clientHeight - 250)
    const jump = h.target.querySelector('[aria-label="Jump to present"]')
    assert.ok(jump)
    jump.click()
    await h.tick()
    assert.equal(calls, 0)
    assert.ok(h.target.scrollHeight - h.target.scrollTop - h.target.clientHeight < 48)
  } finally { await h.close() }
})
