import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'
import type { Notice } from '../api/settings.ts'

// The Home card for a machine notice draws the typed facts of its kind — the
// disk paths and their free space, the provider and its reset, the split
// lanes — and offers the one place the owner can go about it. A notice saved
// before the typed facts shows its stored line.
const rooms = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-notice-card-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: "export { default as NoticeCard } from './NoticeCard.svelte';\nexport { mount, unmount, flushSync } from 'svelte';",
    resolveDir: rooms,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'notice.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

function notice(over: Partial<Notice>): Notice {
  return { id: 'ntc_1', key: 'k', kind: 'disk', severity: 'error', at: new Date().toISOString(), ...over }
}

function render(n: Notice, opts: { action?: { label: string; run: () => void } | null; ondismiss?: () => void } = {}) {
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.NoticeCard, {
    target,
    props: { notice: n, action: opts.action ?? null, ondismiss: opts.ondismiss ?? (() => {}) },
  })
  web.flushSync()
  const text = (target.textContent ?? '').replace(/\s+/g, ' ').trim()
  return { target, text, done: () => { web.unmount(mounted); target.remove() } }
}

test('a disk notice lists the paths, their free space and what cleanup did', () => {
  const view = render(notice({
    disk: { paths: [{ path: '/tmp', freeBytes: 1073741824 }, { path: '/home/a/.dyna', freeBytes: 2048 }], freedBytes: 4096, kept: ['dirty: 2'], maintenanceFailed: true },
  }))
  assert.match(view.text, /Low disk space/)
  assert.match(view.text, /\/tmp 1 GiB free/)
  assert.match(view.text, /\/home\/a\/\.dyna 2 KiB free/)
  assert.match(view.text, /Maintenance freed 4 KiB\./)
  assert.match(view.text, /Kept dirty: 2\./)
  assert.match(view.text, /Some maintenance failed/)
  view.done()
})

test('a provider notice names the profile and when it resets', () => {
  const resetsAt = new Date(Date.now() + 42 * 60000).toISOString()
  const view = render(notice({ kind: 'provider', severity: 'warn', provider: { profile: 'argon', resetsAt } }))
  assert.match(view.text, /Provider limit reached/)
  assert.match(view.text, /argon/)
  assert.match(view.text, /resets in 4[12]m/)
  view.done()
})

test('a reset days away keeps its days on the card', () => {
  // Five minutes of margin keeps the reading stable while the test runs.
  const resetsAt = new Date(Date.now() + 152 * 3600000 + 5 * 60000).toISOString()
  const view = render(notice({ kind: 'provider', severity: 'warn', provider: { profile: 'deepseek', resetsAt } }))
  assert.match(view.text, /resets in 6d 8h/)
  view.done()
})

test('a sync notice names the repository and both lanes, or the rewrite', () => {
  const split = render(notice({ kind: 'sync', severity: 'warn', sync: { repo: '/repo', lane: 'origin', other: 'fork' } }))
  assert.match(split.text, /Remote history split/)
  assert.match(split.text, /origin and fork no longer share a history/)
  split.done()
  const rewrite = render(notice({ kind: 'sync', severity: 'warn', sync: { repo: '/repo', lane: 'fork', rewritten: true } }))
  assert.match(rewrite.text, /Remote history rewritten/)
  assert.match(rewrite.text, /fork was rewritten/)
  rewrite.done()
})

test('a tunnel notice names the missing binary and the PATH it searched', () => {
  const view = render(notice({ kind: 'tunnel', severity: 'error', tunnel: { binary: 'cloudflared', path: '/usr/local/bin:/usr/bin:/bin' } }))
  assert.match(view.text, /Tunnel connector missing/)
  assert.match(view.text, /cloudflared/)
  assert.match(view.text, /not found on PATH \/usr\/local\/bin:\/usr\/bin:\/bin/)
  view.done()
})

test('the card runs its action and dismisses', () => {
  let ran = 0
  let dismissed = 0
  const view = render(notice({ kind: 'provider', severity: 'warn', provider: { profile: 'argon' } }), {
    action: { label: 'Open usage', run: () => { ran++ } },
    ondismiss: () => { dismissed++ },
  })
  const buttons = [...view.target.querySelectorAll('button')]
  const open = need(buttons.find((b) => (b.textContent ?? '').includes('Open usage')), 'the action button')
  open.click()
  assert.equal(ran, 1)
  const dismiss = need(view.target.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]'), 'the dismiss button')
  dismiss.click()
  assert.equal(dismissed, 1)
  view.done()
})

test('a notice saved before the typed facts still shows its line', () => {
  // TODO: legacy/shim remove later — the card keeps the stored line of an old
  // notice until that history ages out.
  const view = render(notice({ kind: 'disk', severity: 'error', text: 'Disk space is low. Maintenance freed 42 bytes.' }))
  assert.match(view.text, /Disk space is low\. Maintenance freed 42 bytes\./)
  view.done()
})
