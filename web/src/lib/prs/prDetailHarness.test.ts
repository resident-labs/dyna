import test from 'node:test'
import assert from 'node:assert/strict'
import { bundle } from './prDetailHarness.ts'

// esm-env's dev fallback reads NODE_ENV at runtime. The production condition
// selects false.js instead, so that fallback must not survive the bundle.
test('the shared browser build resolves esm-env to production, not the dev fallback', async () => {
  const code = Buffer.from(await bundle({ component: '../ui/PhoneKeys' })).toString('utf8')
  assert.equal(code.includes('globalThis.process?.env?.NODE_ENV'), false)
  assert.equal(code.includes("startsWith('prod')"), false)
})
