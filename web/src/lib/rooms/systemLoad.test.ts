import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { setImmediate } from 'node:timers/promises'
import { readFileSync } from 'node:fs'
import { createTestServer } from '../../../test/ssr-server.ts'
import { need } from '../../../test/need.ts'
import { pollLoad, loadView } from './systemLoad.ts'
import type { SystemLoad } from '../api/system.ts'

const sample = (over: Partial<SystemLoad> = {}): SystemLoad => ({
  at: '2026-10-19T12:00:00Z', cpus: 8, load: [59.3, 45.1, 32.1], cpuPercent: 87.4,
  split: { engine: 5, agents: 40, computers: 30, other: 12.4 },
  memory: { total: 16 * 2 ** 30, used: 10 * 2 ** 30, dyna: 4 * 2 ** 30 },
  top: [
    { id: 'ses_a', name: 'lead', address: 'dyna-agent:lead', room: 'dyna-agent', roomId: 'room_1', hue: 3, cores: 2.14 },
    { id: 'wf_x', name: 'wf_x', hue: 0, cores: 0.5 },
  ],
  dynaKnown: true, ...over,
})

// What one test server renders the card with.
interface CardProps {
  load: SystemLoad
}

async function render(t: Parameters<typeof createTestServer>[0], props: CardProps) {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Card } = await vite.ssrLoadModule('/src/lib/rooms/SystemLoadCard.svelte')
  return render(Card, { props }).body
}

test('before two samples the card shows a dash, not 0%', async (t) => {
  const body = await render(t, { load: sample({ cpuPercent: null, split: null, top: [] }) })
  assert.match(body, />–</)
  assert.doesNotMatch(body, /0%/)
})

test('the machine page polls the load only while it shows the local machine', () => {
  const src = readFileSync(new URL('../machines/MachinePage.svelte', import.meta.url), 'utf8')
  assert.match(src, /return pollLoad\(\(\) => api\.systemLoad\(\)/)
  assert.match(src, /<SystemLoadCard title="Usage" \{load\} rows=\{false\}/)
  assert.match(src, /if \(!machine\?\.local\) return/, 'a remote machine has no sampler on this engine')
})

test('polling reads every 5 s and stops on unmount', async () => {
  mock.timers.enable({ apis: ['setInterval'] })
  try {
    let reads = 0
    const seen: number[] = []
    const stop = pollLoad(() => { reads++; return Promise.resolve(reads) }, (v) => seen.push(v))
    assert.equal(reads, 1)
    mock.timers.tick(5000)
    assert.equal(reads, 2)
    await setImmediate()
    stop()
    mock.timers.tick(15000)
    assert.equal(reads, 2)
    assert.deepEqual(seen, [1, 2])
  } finally {
    mock.timers.reset()
  }
})

test('a slow answer that arrives after a newer one is dropped', async () => {
  mock.timers.enable({ apis: ['setInterval'] })
  try {
    const answers: ((value: string) => void)[] = []
    const seen: unknown[] = []
    const stop = pollLoad(() => new Promise((r) => answers.push(r)), (v) => seen.push(v))
    mock.timers.tick(5000)
    need(answers[1])('new')
    await setImmediate()
    need(answers[0])('old')
    await setImmediate()
    stop()
    assert.deepEqual(seen, ['new'])
  } finally {
    mock.timers.reset()
  }
})

test('an answer after unmount is ignored', async () => {
  mock.timers.enable({ apis: ['setInterval'] })
  try {
    let answer: (value: string) => void = () => {}
    const seen: unknown[] = []
    const stop = pollLoad(() => new Promise((r) => { answer = r }), (v) => seen.push(v))
    stop()
    answer('late')
    await setImmediate()
    assert.deepEqual(seen, [])
  } finally {
    mock.timers.reset()
  }
})
