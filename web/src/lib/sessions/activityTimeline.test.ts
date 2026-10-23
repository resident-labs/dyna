import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { domNames, installDom, load } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'
import type { TimelineRow } from './dm.ts'

const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')

test('steps expand independently, retain failures and load shell logs only when opened', async () => {
  const dom = installDom()
  Reflect.set(globalThis, 'HTMLMediaElement', dom.window.HTMLMediaElement)
  const calls: string[] = []
  Reflect.set(globalThis, 'timelineLogCalls', calls)
  const { dir, default: C, mount, unmount, flushSync } = await load({
    component: '../sessions/ActivityTimeline', format,
    api: "export const api = { shellLogRange: (id) => { globalThis.timelineLogCalls.push(id); return Promise.resolve({ text: 'complete shell output', next: 21, size: 21 }) } }; export const copyText = async () => {}",
  })
  const started = '2026-10-23T07:00:00Z'
  const row: TimelineRow = { kind: 'timeline', key: 'timeline:14', turn: { n: 14, started, ended: '2026-10-23T07:02:10Z', status: 'ok' }, steps: [
    { kind: 'tool', key: 'read', tool: 'read', target: 'web/src/a/very/long/path/with/the/full/target.ts' },
    { kind: 'shell', key: 'shell', jobId: 'sh_1', waits: 0, posts: [], job: { id: 'sh_1', command: ['bash', '-c', 'npm test'], name: 'Run tests', cwd: '/', state: 'exited', code: 0, started, ended: '2026-10-23T07:00:01Z' } },
    { kind: 'tool', key: 'failed', tool: 'ran', target: 'npm test', failed: true, command: 'npm test', output: 'expected 1 call, got 0', exit: 1 },
  ] }
  let app
  try {
    flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
    assert.match(document.body.textContent ?? '', /Turn 14.*3 steps.*2m 10s/)
    assert.match(document.body.textContent ?? '', /expected 1 call, got 0/)
    assert.deepEqual(calls, [])
    const steps = document.querySelectorAll('details')
    // A shell step names what ran and how it ended, not the tool name and code.
    const shellStep = need(steps[1])
    assert.equal(need(shellStep.querySelector('.verb')).textContent, 'Shell command')
    assert.equal(need(shellStep.querySelector('.meta')).textContent, 'succeeded in 1s')
    assert.doesNotMatch(shellStep.textContent ?? '', /exit 0/)
    const expand = (index: number) => flushSync(() => {
      const step = need(steps[index])
      step.open = true
      step.dispatchEvent(new Event('toggle'))
    })
    expand(0)
    assert.equal(need(document.querySelector('.full-target')).textContent, row.steps[0]?.target)
    assert.equal(need(steps[1]).open, false)
    expand(1)
    await Promise.resolve()
    flushSync()
    assert.deepEqual(calls, ['sh_1'])
    assert.match(document.body.textContent ?? '', /complete shell output/)
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const key of [...domNames, 'HTMLMediaElement', 'timelineLogCalls']) Reflect.deleteProperty(globalThis, key)
    await rm(dir, { recursive: true, force: true })
  }
})

test('an active tool stays running when the API omits its false done flag', async () => {
  const dom = installDom()
  Reflect.set(globalThis, 'HTMLMediaElement', dom.window.HTMLMediaElement)
  const { dir, default: C, mount, unmount, flushSync } = await load({ component: '../sessions/ActivityTimeline', format })
  let app
  try {
    for (const status of ['running', 'ok']) {
      const row: TimelineRow = { kind: 'timeline', key: 'timeline:1', turn: { n: 1, status, started: '2026-10-23T07:00:00Z' }, steps: [
        { kind: 'tool', key: 'active', tool: 'read', target: 'active.ts', aside: '2 files' },
        { kind: 'tool', key: 'complete', tool: 'read', target: 'complete.ts', done: true },
        { kind: 'tool', key: 'failed', tool: 'ran', target: 'npm test', failed: true },
      ] }
      flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
      const states = [...document.querySelectorAll('.timeline li')].map((li) => li.querySelector('.dot')?.getAttribute('aria-label'))
      assert.deepEqual(states, [status === 'running' ? 'running' : 'done', 'done', 'failed'])
      assert.equal(document.querySelector('.running .meta')?.textContent, status === 'running' ? '2 files · running' : undefined)
      assert.equal(document.querySelector('.failed .meta')?.textContent, 'failed')
      if (app) unmount(app)
      app = undefined
    }
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const key of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, key)
    await rm(dir, { recursive: true, force: true })
  }
})

test('a running turn measures elapsed time despite the API zero end time', async (t) => {
  t.mock.method(Date, 'now', () => Date.parse('2026-10-23T07:02:10Z'))
  const dom = installDom()
  Reflect.set(globalThis, 'HTMLMediaElement', dom.window.HTMLMediaElement)
  const { dir, default: C, mount, unmount, flushSync } = await load({ component: '../sessions/ActivityTimeline', format })
  const row: TimelineRow = { kind: 'timeline', key: 'timeline:1', turn: { n: 1, status: 'running', started: '2026-10-23T07:00:00Z', ended: '0001-01-01T00:00:00Z' }, steps: [] }
  let app
  try {
    flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
    assert.match(document.querySelector('.timeline header')?.textContent ?? '', /2m 10s/)
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const key of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, key)
    await rm(dir, { recursive: true, force: true })
  }
})
