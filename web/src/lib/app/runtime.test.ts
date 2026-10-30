import test from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime } from './runtime.svelte.ts'
import { runtimeFixture } from '../../../test/runtime.ts'
import type { BootResult } from '../settings/link.ts'

test('destroy before boot resolves prevents subscription and cleanup runs once', async () => {
  let resolveBoot: (value: BootResult | null) => void = () => {}
  const boot = new Promise<BootResult | null>((resolve) => { resolveBoot = resolve })
  let starts = 0, destroys = 0, popped = 0, unauthorized = 0
  const fixture = runtimeFixture({
    unauthorized: () => { unauthorized++ }, loadProfiles: () => { starts++ }, destroyData: () => { destroys++ }, popped: () => { popped++ },
  })
  fixture.env.boot = () => boot
  fixture.env.connect = () => { starts++; return () => {} }
  const stop = createRuntime(fixture.host, fixture.env).start()
  assert.equal(fixture.timers.size, 3)
  stop()
  stop()
  fixture.unauthorized()
  fixture.env.window.dispatchEvent(new fixture.dom.window.Event('dyna:computer-popped'))
  resolveBoot(null)
  await boot
  await Promise.resolve()
  assert.equal(starts, 0)
  assert.equal(destroys, 1)
  assert.equal(popped, 0)
  assert.equal(unauthorized, 0)
  assert.equal(fixture.timers.size, 0)
})
