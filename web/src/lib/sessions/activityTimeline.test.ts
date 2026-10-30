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
    { kind: 'tool', key: 'read', step: 'read', label: 'target.ts', durationMs: 12, tool: 'read', target: 'web/src/a/very/long/path/with/the/full/target.ts' },
    { kind: 'shell', key: 'shell', jobId: 'sh_1', waits: 0, posts: [], job: { id: 'sh_1', command: ['bash', '-c', 'npm test'], name: 'Run tests', cwd: '/', state: 'exited', code: 0, started, ended: '2026-10-23T07:00:01Z' } },
    { kind: 'tool', key: 'failed', step: 'run', label: 'npm', tool: 'ran', target: 'npm test', failed: true, command: 'npm test', output: 'expected 1 call, got 0', exit: 1 },
  ] }
  let app
  try {
    flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
    assert.match(document.body.textContent ?? '', /3 steps/)
    assert.match(document.body.textContent ?? '', /expected 1 call, got 0/)
    assert.deepEqual(calls, [])
    const steps = document.querySelectorAll('details')
    // A tool step shows a designed kind, a short label and a state chip; the
    // raw tool name, the full target and the exit code wait in its details.
    assert.equal(need(steps[0]).querySelector('.label')?.textContent, 'target.ts')
    assert.ok(need(steps[0]).querySelector('svg'), 'the step lost its kind icon')
    assert.doesNotMatch(need(steps[0]).querySelector('summary')?.textContent ?? '', /web\/src/)
    // A shell step names what ran and how it ended, not the tool name and code.
    const shellStep = need(steps[1])
    assert.equal(need(shellStep.querySelector('.verb')).textContent, 'Shell command')
    assert.equal(need(shellStep.querySelector('.meta')).textContent, 'succeeded in 1s')
    assert.doesNotMatch(shellStep.textContent ?? '', /exit 0/)
    // A failure is a state chip, never a bare exit code.
    assert.equal(need(steps[2]).querySelector('.status.danger')?.textContent, 'failed')
    assert.doesNotMatch(need(steps[2]).querySelector('summary')?.textContent ?? '', /exit 1/)
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
        { kind: 'tool', key: 'active', step: 'read', label: 'active.ts', tool: 'read', target: 'active.ts', aside: '2 files' },
        { kind: 'tool', key: 'complete', step: 'read', label: 'complete.ts', tool: 'read', target: 'complete.ts', done: true },
        { kind: 'tool', key: 'failed', step: 'run', label: 'npm', tool: 'ran', target: 'npm test', failed: true },
      ] }
      flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
      const steps = [...document.querySelectorAll('.timeline li')]
      assert.equal(steps[0]?.querySelector('.status.dot')?.textContent, status === 'running' ? 'running' : undefined)
      assert.equal(steps[1]?.querySelector('.status'), null)
      assert.equal(steps[2]?.querySelector('.status.danger')?.textContent, 'failed')
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
