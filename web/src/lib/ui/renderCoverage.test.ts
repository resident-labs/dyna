import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

// Build the shared component graph once. Tests measure rendering and assertions,
// while each case uses the real components and markdown sanitizer.
const names = [...domNames, 'DOMParser', 'NodeFilter']
const saved = new Map(names.map((name) => [name, Object.getOwnPropertyDescriptor(globalThis, name)]))
const dom = installDom()
const window: unknown = Reflect.get(dom, 'window')
assert.ok(typeof window === 'object' && window !== null)
Object.assign(globalThis, { DOMParser: Reflect.get(window, 'DOMParser'), NodeFilter: Reflect.get(window, 'NodeFilter') })
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-render-coverage-'))
after(async () => {
  const close: unknown = Reflect.get(window, 'close')
  if (typeof close === 'function') close.call(window)
  for (const [name, descriptor] of saved) {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor)
    else Reflect.deleteProperty(globalThis, name)
  }
  await rm(dir, { recursive: true, force: true })
})
const bundle = await build({
  entryPoints: [fileURLToPath(new URL('../../../test/render-fixture.ts', import.meta.url))],
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
  loader: { '.svg': 'dataurl', '.png': 'dataurl' },
  plugins: [svelteEsbuildPlugin('server')],
})
const code = bundle.outputFiles[0]
assert.ok(code)
const output = join(dir, 'render.mjs')
await writeFile(output, code.contents)
const fixture: unknown = await import(pathToFileURL(output).href)
assert.ok(typeof fixture === 'object' && fixture !== null)
const checkedFixture = fixture

function render(name: string): unknown {
  const fn: unknown = Reflect.get(checkedFixture, name)
  assert.equal(typeof fn, 'function')
  assert.ok(typeof fn === 'function')
  const result: unknown = fn()
  return result
}

function html(name: string): string {
  const result = render(name)
  assert.ok(typeof result === 'string')
  return result
}

test('loading views show a message-shaped skeleton that names what loads', () => {
  const body = html('skeleton')
  assert.match(body, /role="status"[^>]*aria-label="Loading room"|aria-label="Loading room"[^>]*role="status"/)
  assert.equal((body.match(/class="sk-row/g) ?? []).length, 3)
  const session = html('loadingSession')
  assert.match(session, /aria-label="Loading session"/)
  assert.doesNotMatch(session, /Loading session…/)
})

test('the feed and the session view show the same attachment card', () => {
  const bodies = render('attachmentRenderings')
  assert.ok(Array.isArray(bodies))
  assert.equal(bodies.length, 4)
  for (const body of bodies) {
    assert.ok(typeof body === 'string')
    assert.ok(body.includes('Plan is ready.'))
    assert.ok(body.includes('Open home-design.md'))
    assert.ok(!body.includes('file-reading tool'))
    assert.ok(!body.includes('dyna attachment get'))
    assert.ok(!body.includes('/home/a/rooms'))
  }
  const card = (body: unknown): string => {
    assert.ok(typeof body === 'string')
    const fragment = document.createElement('div')
    fragment.innerHTML = body
    const file = fragment.querySelector('button.file')
    assert.ok(file, 'the attachment renders an actual file card')
    return file.outerHTML.replace(/\s+/g, ' ')
  }
  assert.equal(card(bodies[0]), card(bodies[1]))
  assert.equal(card(bodies[2]), card(bodies[3]))
})

test('raw delivered prompts show an attachment card without agent instructions', () => {
  const body = html('rawAttachment')
  assert.ok(body.includes('Open home-design.md'))
  assert.ok(!body.includes('file-reading tool'))
})

test('HTML cards render as banners and visual updates render as chips', () => {
  const card = html('htmlCard')
  assert.match(card, /class="banner[ "]/)
  assert.ok(!card.includes('Text file'))
  const update = html('visualUpdate')
  assert.match(update, /class="chip[ "]/)
  assert.ok(update.includes('Updated <strong'))
})

test('a visual chip uses the current title instead of the old version title', () => {
  assert.match(html('currentVisualTitle'), /CI time per PR<\/strong> to v2/)
})


test('a feed with a call row hides the empty message and an empty feed keeps it', () => {
  const calls = html('callOnlyFeed')
  assert.match(calls, /Review the call request/)
  assert.doesNotMatch(calls, /No messages yet/)
  assert.match(html('emptyFeed'), /No messages yet/)
})
