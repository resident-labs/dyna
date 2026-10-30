import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime } from '../app/runtime.svelte.ts'
import { runtimeFixture } from '../../../test/runtime.ts'
import type { BootResult } from '../settings/link.ts'

function mounted() {
  let needPair = false, starts = 0, stops = 0, destroys = 0
  let finishBoot: (value: BootResult | null) => void = () => {}
  const boot = new Promise<BootResult | null>((resolve) => { finishBoot = resolve })
  const fixture = runtimeFixture({
    needPair: () => needPair, unauthorized: () => { needPair = true }, destroyData: () => { destroys++ },
  })
  fixture.env.boot = () => boot
  fixture.env.connect = () => { starts++; return () => { stops++ } }
  const unmount = createRuntime(fixture.host, fixture.env).start()
  return {
    ...fixture, unmount, needPair: () => needPair, starts: () => starts, stops: () => stops,
    destroys: () => destroys, finishBoot: async () => { finishBoot(null); await boot; await Promise.resolve() },
  }
}

test('a 401 opens login and stops event work, polling and data controllers', async () => {
  const app = mounted()
  await app.finishBoot()
  assert.equal(app.starts(), 1)
  assert.equal(app.timers.size, 3)
  app.unauthorized()
  assert.equal(app.needPair(), true)
  assert.equal(app.stops(), 1)
  assert.equal(app.destroys(), 1)
  assert.equal(app.timers.size, 0)
  app.unauthorized()
  app.unmount()
  assert.equal(app.stops(), 1)
  assert.equal(app.destroys(), 1)
})

test('a 401 during boot cannot start the app after login opens', async () => {
  const app = mounted()
  app.unauthorized()
  await app.finishBoot()
  assert.equal(app.needPair(), true)
  assert.equal(app.starts(), 0)
  assert.equal(app.timers.size, 0)
})
