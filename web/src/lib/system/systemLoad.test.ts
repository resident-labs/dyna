import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { setImmediate } from 'node:timers/promises'
import { readFileSync } from 'node:fs'
import { need } from '../../../test/need.ts'
import { pollLoad } from './systemLoad.ts'
test('the machine page polls the load only while it shows the local machine', () => {
  const src = readFileSync(new URL('../machines/MachinePage.svelte', import.meta.url), 'utf8')
  assert.match(src, /return pollLoad\(\(\) => api\.systemLoad\(\)/)
  assert.match(src, /<NodeUsageCard \{machine\} \{load\}/)
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
