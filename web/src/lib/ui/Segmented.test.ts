import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

const read = (p: string): string => readFileSync(new URL(p, import.meta.url), 'utf8')

test('every one-of-a-few toggle uses the shared Segmented, with no local .seg copy', () => {
  for (const f of ['../files/Viewer.svelte', '../files/FileView.svelte', '../prs/GitHub.svelte', '../settings/SettingsPane.svelte']) {
    const text = read(f)
    assert.match(text, /<Segmented\b/, f)
    assert.doesNotMatch(text, /class="seg"|\.seg\b/, f)
  }
})

test('arrow keys move the choice and wrap around', () => {
  const source = read('./Segmented.svelte')
  const picked: string[] = []
  const context: Record<string, unknown> = {
    options: [['a', 'A'], ['b', 'B'], ['c', 'C']],
    value: 'a',
    onchange: (v: string) => picked.push(v),
  }
  runFunctions(source, ['key'], context)
  const press = (key: string): void => {
    call(context, 'key', { key, preventDefault() {}, currentTarget: { querySelectorAll: () => [] } })
  }
  press('ArrowRight')
  press('ArrowLeft')
  press('Tab')
  assert.deepEqual(picked, ['b', 'c'])
})
