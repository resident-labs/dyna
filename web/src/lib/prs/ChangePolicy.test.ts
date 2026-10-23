import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import type { PolicyOverride } from '../api/change.ts'
import { parseChecks, buildOverride, overrideKeys, normalizePolicy, saveRoomPolicy, saveOverride, resetOverride, DEFAULT_POLICY, budgetForm, parseTestAllow, type NormalizedPolicy } from './pr.ts'
import { need } from '../../../test/need.ts'

async function load(name: string) {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-policy-ui-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [svelteEsbuildPlugin('server'), { name: 'policy-fixture', setup(builder) {
      builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, `${name}.mjs`)
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')
const room = { ...DEFAULT_POLICY }
const formOf = (p: Pick<NormalizedPolicy, 'requiredChecks' | 'review'>) => ({ requiredChecks: p.requiredChecks.join(', '), review: p.review })
type Call = ['room', string, NormalizedPolicy] | ['override', string, string, Partial<PolicyOverride>]
const mock = () => {
  const calls: Call[] = []
  return {
    calls,
    setRoomChangePolicy: async (roomId: string, policy: NormalizedPolicy) => { calls.push(['room', roomId, policy]) },
    setChangePolicyOverride: async (roomId: string, changeId: string, override: Partial<PolicyOverride>) => { calls.push(['override', roomId, changeId, override]) },
  }
}
// sentPolicy is the policy of the room call at an index.
function sentPolicy(bridge: ReturnType<typeof mock>, index: number): NormalizedPolicy {
  const call = need(bridge.calls[index])
  if (call[0] !== 'room') throw new Error('the call is not a room policy call')
  return call[2]
}

test('required checks parse from comma text', () => {
  assert.deepEqual(parseChecks(' a, b ,,a\nc '), ['a', 'b', 'c'])
  assert.deepEqual(parseChecks(''), [])
  assert.deepEqual(normalizePolicy(null), DEFAULT_POLICY)
})

test('room policy save sends the whole policy with a check list', async () => {
  const b = mock()
  const legacy = { ...formOf(room), checks: 'local', review: 'off', land: 'branch-only', requiredChecks: 'x, y' }
  const sent = await saveRoomPolicy(b, 'r', legacy)
  assert.deepEqual(b.calls, [['room', 'r', { requiredChecks: ['x', 'y'], review: 'off' }]])
  assert.equal(sent.requiredChecks.length, 2)
})

test('override sends only differing settings and needs a reason', async () => {
  const b = mock()
  await assert.rejects(saveOverride(b, 'r', 'c1', room, { ...formOf(room), review: 'off' }, '  '), /Say why/)
  assert.equal(b.calls.length, 0)
  await saveOverride(b, 'r', 'c1', room, { ...formOf(room), review: 'off', requiredChecks: 'unit' }, ' docs only ')
  assert.deepEqual(b.calls, [['override', 'r', 'c1', { reason: 'docs only', review: 'off', requiredChecks: ['unit'] }]])
})

test('an override that matches the room policy resets', async () => {
  const b = mock()
  await saveOverride(b, 'r', 'c1', room, formOf(room), 'ignored')
  await resetOverride(b, 'r', 'c1')
  assert.deepEqual(b.calls, [['override', 'r', 'c1', { reason: '' }], ['override', 'r', 'c1', { reason: '' }]])
  assert.deepEqual(overrideKeys({ reason: 'x' }), [])
  const legacy = { checks: 'none', requiredChecks: [], reason: 'x' }
  assert.deepEqual(overrideKeys(legacy), ['requiredChecks'])
  assert.deepEqual(buildOverride(room, formOf(room), 'r'), { reason: 'r' })
})


test('change policy shows own rules, override reason and skips', async () => {
  const { dir, component, render } = await load('ChangePolicy')
  try {
    const change = { changeId: 'c1', state: 'open', policy: { ...room, review: 'off' }, policyOverride: { review: 'off', reason: 'docs only' }, skipReasons: ['docs only'] }
    const t = text(render(component, { props: { change, room, roomId: 'r' } }).body)
    assert.match(t, /This PR own rules/)
    assert.match(t, /Review Off/)
    assert.doesNotMatch(t, /Landing/)
    assert.match(t, /Reason docs only/)
    assert.match(t, /Skipped docs only/)
    assert.match(t, /Edit this PR's rules/)
    assert.match(t, /Reset to room policy/)
    const plain = text(render(component, { props: { change: { changeId: 'c2', state: 'open', policy: room }, room, roomId: 'r' } }).body)
    assert.match(plain, /Uses the room rules/)
    assert.match(plain, /Change rules for this PR/)
    assert.doesNotMatch(plain, /Reset to room policy/)
    assert.doesNotMatch(plain, /Merge pull request/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('budget defaults preserve false and exact allowlist names', async () => {
  assert.deepEqual(budgetForm(null), { ciTimeBudget: 'warn', ciBudgetExempt: '', ciWarnAfter: '2m', ciFailAfter: '3m', ciTestMax: '10s', ciTestAllow: '', ciTestAllowNotes: Object.create(null), ciNoJUnitWarning: true })
  const names = ['pkg.Test/name, with comma', 'suite::test[param value]']
  const form = budgetForm({ ...room, ciNoJUnitWarning: false, ciWarnAfter: '', ciTestAllow: names })
  assert.equal(form.ciNoJUnitWarning, false)
  assert.equal(form.ciWarnAfter, '2m')
  assert.deepEqual(parseTestAllow(form.ciTestAllow), names)
  assert.deepEqual(parseTestAllow('A\r\nB,C\nA\n'), ['A', 'B,C'])
  assert.deepEqual(parseTestAllow(' A \n   \n pkg/Test name #1 '), ['A', 'pkg/Test name #1'])
  const b = mock()
  await saveRoomPolicy(b, 'r', { ...form, requiredChecks: 'check' })
  assert.equal(sentPolicy(b, 0).ciNoJUnitWarning, false)
  assert.deepEqual(sentPolicy(b, 0).ciTestAllow, names)
  assert.deepEqual(sentPolicy(b, 0).requiredChecks, ['check'])
})

test('budget fields show defaults, exact names, and disabled controls while saving', async () => {
  const { dir, component, render } = await load('CIBudgetFields')
  try {
    const defaults = render(component, { props: { form: budgetForm(null) } }).body
    for (const label of ['Time budget', 'Warn', 'Enforce', 'Run warning', 'Run failure', 'Per-test maximum', 'Exempt workflows', 'Slow-test allowlist', 'No-JUnit warning']) assert.ok(defaults.includes(label))
    for (const value of ['2m', '3m', '10s']) assert.ok(defaults.includes(`value="${value}"`))
    assert.match(defaults, /type="checkbox"[^>]*checked/)
    const edited = render(component, { props: { form: budgetForm({ ciWarnAfter: '4m', ciFailAfter: '5m', ciTestMax: '20s', ciNoJUnitWarning: false, ciTestAllow: ['pkg.Test/a,b', 'Suite.test[x]'], ciTestAllowNotes: { 'pkg.Test/a,b': 'External service <slow>', 'old.test': 'Removed test' } }), busy: true } }).body
    assert.match(edited, /pkg\.Test\/a,b\nSuite\.test\[x\]/)
    assert.match(edited, /<code>pkg\.Test\/a,b<\/code>: measured time and reason/)
    assert.match(edited, /External service/)
    assert.doesNotMatch(edited, /It allows this test name in every package/)
    assert.match(edited, /one exact test ID per line/)
    assert.match(edited, /This allowance has no measured time or reason/)
    assert.doesNotMatch(edited, /Removed test/)
    assert.doesNotMatch(edited, /type="checkbox"[^>]*checked/)
    assert.equal((edited.match(/ disabled/g) || []).length, 9)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('new allowances require notes, old allowances remain valid, and removed notes are pruned', async () => {
  const b = mock()
  const original: NormalizedPolicy = { ...room, ciTestAllow: ['legacy'], ciTestAllowNotes: { removed: 'old debt' } }
  const form = { ...budgetForm(original), requiredChecks: 'check', ciTestAllow: ' legacy \n pkg/Test name #1 ' }
  await assert.rejects(saveRoomPolicy(b, 'r', form, original), /measured time and reason for pkg\/Test name #1/)
  assert.equal(b.calls.length, 0)
  form.ciTestAllowNotes['pkg/Test name #1'] = '  12 seconds: external service  '
  const next = await saveRoomPolicy(b, 'r', form, original)
  assert.deepEqual(next.ciTestAllow, ['legacy', 'pkg/Test name #1'])
  assert.deepEqual(next.ciTestAllowNotes, { 'pkg/Test name #1': '12 seconds: external service' })
  form.ciTestAllow = 'legacy'
  const cleared = await saveRoomPolicy(b, 'r', form, original)
  assert.deepEqual(cleared.ciTestAllowNotes, {})
})


test('workflow exemptions save exact names and can be cleared', async () => {
  const bridge = mock()
  const form = { ...budgetForm({ ...room, ciBudgetExempt: ['Long build, Linux'] }), requiredChecks: '' }
  assert.equal(form.ciBudgetExempt, 'Long build, Linux')
  form.ciBudgetExempt = ' Long build, Linux \n Integration #1 \n Long build, Linux '
  await saveRoomPolicy(bridge, 'r', form)
  assert.deepEqual(sentPolicy(bridge, 0).ciBudgetExempt, ['Long build, Linux', 'Integration #1'])
  form.ciBudgetExempt = ''
  await saveRoomPolicy(bridge, 'r', form)
  assert.deepEqual(sentPolicy(bridge, 1).ciBudgetExempt, [])
})

test('retired check modes and local names do not reach policy controls or requests', async () => {
  const old = { land: 'github-pr', checks: 'local', requiredChecks: ['go-test-touched', 'unit', 'frontend-if-touched'], review: 'on' }
  assert.deepEqual(normalizePolicy(old), { ...DEFAULT_POLICY, requiredChecks: ['unit'] })
  const bridge = mock()
  await saveRoomPolicy(bridge, 'r', formOf(old))
  assert.deepEqual(sentPolicy(bridge, 0), { ...DEFAULT_POLICY, requiredChecks: ['unit'] })
  const source = readFileSync(new URL('../rooms/RoomSettings.svelte', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /Review starts after|POLICY_CHECKS|form\.checks/)
})

test('room time budget mode saves through the existing policy', async () => {
  const b = mock()
  await saveRoomPolicy(b, 'r', { ...budgetForm({ ciTimeBudget: 'enforce' }), requiredChecks: '' })
  assert.equal(sentPolicy(b, 0).ciTimeBudget, 'enforce')
})
