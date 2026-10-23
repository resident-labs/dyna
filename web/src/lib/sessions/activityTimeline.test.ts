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
    { kind: 'shell', key: 'shell', jobId: 'sh_1', waits: 0, posts: [], job: { id: 'sh_1', command: ['bash', '-c', 'npm test'], name: 'Run tests', cwd: '/', state: 'exited', code: 0, started, ended: started } },
    { kind: 'tool', key: 'failed', tool: 'ran', target: 'npm test', failed: true, command: 'npm test', output: 'expected 1 call, got 0', exit: 1 },
  ] }
  let app
  try {
    flushSync(() => app = mount(C, { target: need(document.getElementById('app')), props: { row } }))
    assert.match(document.body.textContent ?? '', /Turn 14.*3 steps.*2m 10s/)
    assert.match(document.body.textContent ?? '', /expected 1 call, got 0/)
    assert.deepEqual(calls, [])
    const steps = document.querySelectorAll('details')
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
