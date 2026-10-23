import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
const code = await bundle({ component: '../rooms/Pins', format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8') })
test('pin search matches text, speaker and attachment names without changing pin order', async () => {
  const dom = installDom()
  const extra = ['HTMLMediaElement', 'DOMParser', 'NodeFilter']
  for (const name of extra) globalThis[name] = dom.window[name]
  const ui = await importBundle(code, '../rooms/Pins')
  const pins = [
    { id: 'p1', text: 'Deploy rule', speaker: 'Leo' },
    { id: 'p2', text: 'Release checklist', speaker: 'Maya' },
    { id: 'p3', text: 'Weekly data', speaker: 'Leo', attachments: [{ id: 'f1', name: 'report.csv', title: 'Quarterly figures', mime: 'text/csv', size: 12 }] },
  ].map(p => ({ ts: 1761000000000, pinnedAt: 1761000000000, ...p }))
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: { room: { id: 'global', name: 'work' }, pins, onjump() {}, onunpin() {}, onclose() {} } })
  try {
    ui.flushSync()
    const field = document.querySelector('input')
    const filter = query => { field.value = query; field.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync() }
    for (const [query, expected] of [['RULE', 'Deploy rule'], ['maya', 'Release checklist'], ['report.csv', 'Weekly data'], ['Quarterly', 'Weekly data']]) {
      filter(query); assert.equal(document.querySelectorAll('article').length, 1)
      assert.ok(document.querySelector('article').textContent.includes(expected))
      assert.equal(document.querySelector('.matches').textContent, '1 of 3 pinned posts')
    }
    filter('missing'); assert.equal(document.querySelectorAll('article').length, 0)
    assert.match(document.querySelector('.none').textContent, /No pinned posts match/)
    document.querySelector('[aria-label="Clear filter"]').click(); ui.flushSync()
    assert.deepEqual([...document.querySelectorAll('article .prose')].map(p => p.textContent.trim()), ['Deploy rule', 'Release checklist', 'Weekly data'])
  } finally {
    await ui.unmount(component); dom.window.close()
    for (const name of [...domNames, ...extra]) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
