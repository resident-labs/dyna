import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { blankProfile, withEffort, type EditableProfile } from './profiles/state.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('./ProfileWizard.svelte', import.meta.url), 'utf8')

test('changing model keeps the compact target and changing harness resets it', () => {
  const high = { id: 'high', args: ['--reasoning-effort', 'high'] }
  const context: { profileDraft: Pick<EditableProfile, 'harness' | 'compact' | 'extraArgs'>; editorDraft: { args: string }; exactId: string; [name: string]: unknown } = {
    blankProfile, withEffort, lines: (s: string) => s.split('\n').map((x) => x.trim()).filter(Boolean),
    catalog: { models: [{ id: 'grok-4.7', efforts: [high] }] },
    exactId: 'grok-4.7#high',
    profileDraft: { harness: 'grok', compact: { tokens: 95000, windows: { 'grok-4.7': 200000 } }, extraArgs: ['--no-plan', '--reasoning-effort', 'high'] },
    editorDraft: { args: '--no-plan\n--reasoning-effort\nhigh' },
  }
  runFunctions(source, ['forgetKnobs'], context)
  call(context, 'forgetKnobs')
  assert.deepEqual([...(context.profileDraft.extraArgs ?? [])], ['--no-plan'])
  assert.equal(context.editorDraft.args, '--no-plan')
  assert.deepEqual(context.profileDraft.compact, { tokens: 95000, windows: { 'grok-4.7': 200000 } })
  assert.equal(context.exactId, '')
  call(context, 'forgetKnobs', 'pi')
  assert.deepEqual(context.profileDraft.compact, { tokens: 200000 })
  call(context, 'forgetKnobs', 'grok')
  assert.equal(context.profileDraft.compact, null)
})
