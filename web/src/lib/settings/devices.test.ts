import test from 'node:test'
import assert from 'node:assert/strict'
import { deviceName, scopeChips } from './devices.ts'

// A device row names the device and shows one chip per scope, so the owner
// reads labels and never the engine's joined key list or session stamp.
test('scopes become one labelled chip each and device names drop the session stamp', () => {
  const chips = scopeChips({ scopes: ['read', 'terminal'] })
  assert.deepEqual(chips.map((chip) => chip.label), ['Read', 'Terminal'])
  assert.ok(chips.every((chip) => chip.about.length > 0), 'each chip explains its scope')

  assert.equal(scopeChips({ legacy: true, scopeLabel: 'legacy: full access' }).length, 6, 'a legacy grant holds every scope')
  assert.equal(scopeChips({ scopes: [] })[0]?.label, 'No access')

  const browser = deviceName({ name: 'dyna browser · 20261023-112744-931569b1', origin: 'http://127.0.0.1:7777' })
  assert.equal(browser.title, 'dyna browser · 127.0.0.1:7777')
  assert.equal(browser.stamp, '20261023-112744-931569b1')

  assert.deepEqual(deviceName({ name: 'Chrome' }), { title: 'Chrome', stamp: '', tooltip: '' })
  assert.equal(deviceName({ name: '' }).title, 'Unnamed device')
})
