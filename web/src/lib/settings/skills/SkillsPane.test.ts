import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { errorText } from '../../errorText.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'
import type { HarnessConfig, ManagedSkill } from '../../api/harness.ts'

const read = (path: string): string => readFileSync(new URL(path, import.meta.url), 'utf8')
const pane = read('./SkillsPane.svelte')
const form = read('./SkillForm.svelte')

const catalog = (skills: ManagedSkill[] = []): HarnessConfig => ({ mcp: [], skills })
const helpers = { errorText, failText: (e: unknown, fallback: string) => (e ? errorText(e) : fallback) }

test('the skills switch sends one global change', async () => {
  const calls: unknown[][] = []
  const context: { catalog: HarnessConfig | null; notice: string; [name: string]: unknown } = {
    ...helpers, busy: false, stopped: false, error: '', notice: '', catalog: catalog([{ name: 'review', source: '/skills/review', enabled: false }]),
    api: { setHarnessConfigEnabled: async (...args: unknown[]) => { calls.push(args); return catalog() } },
  }
  runFunctions(pane, ['setEnabled'], context)
  await call(context, 'setEnabled', { name: 'review', source: '/skills/review', enabled: false }, true)
  assert.deepEqual(calls, [['skill', 'review', '', true]])
})

test('a new skill starts enabled', async () => {
  const sent: ManagedSkill[] = []
  const context: { name: string; source: string; error: string; [name: string]: unknown } = {
    errorText, busy: false, error: '', name: ' review ', source: ' /skills/review',
    api: { addHarnessSkill: async (entry: ManagedSkill) => { sent.push(entry); return catalog() } },
    onsave: () => {},
  }
  runFunctions(form, ['save'], context)
  await call(context, 'save')
  assert.equal(JSON.stringify(sent), JSON.stringify([{ name: 'review', source: '/skills/review', enabled: true }]))
})
