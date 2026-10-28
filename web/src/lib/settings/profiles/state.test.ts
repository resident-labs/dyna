import test from 'node:test'
import assert from 'node:assert/strict'
import { filterModels, selectedAccount, routeModel, blankProfile, normalizeModels, withEffort, effortIn, routeEffort, profileEffort, chooseEffort, compactBlock, compactFor, compactWindows, compactHint } from './state.ts'
import { catalogModel as model, effortOf as effort, account as accountFixture } from '../../../../test/account-fixtures.ts'
import { need } from '../../../../test/need.ts'

const go = model('opencode-go/model', { label: 'Shared name', providerId: 'opencode-go', category: 'go' })
const free = model('opencode/model-free', { label: 'Shared name', providerId: 'opencode', category: 'free' })
const vendor = model('vendor/model', { label: 'Other', providerId: 'vendor', publisher: 'Publisher', category: 'other' })
const models = [go, free, vendor]
test('model filters use categories and provider routes', () => {
  assert.deepEqual(filterModels(models, { category: 'go' }), [go])
  assert.deepEqual(filterModels(models, { category: 'free' }), [free])
  assert.deepEqual(filterModels(models, { category: 'all', search: ' PUBLISHER ' }), [vendor])
  assert.deepEqual(filterModels(models, { category: 'go', provider: 'opencode' }), [])
})
test('route preselection preserves exact identity and never substitutes by label', () => {
  assert.equal(routeModel(models, 'opencode/model-free'), free)
  assert.equal(routeModel(models, 'Shared name'), null)
  assert.equal(routeModel(models, ''), null)
})
test('account selection requires matching harness, route, and unambiguous active connection', () => {
  const account = accountFixture({ id: 'a', harness: 'pi', providerId: 'opencode-go', active: true })
  assert.equal(selectedAccount([account], 'pi', go), account)
  assert.equal(selectedAccount([account], 'claude-code', go), null)
  assert.equal(selectedAccount([account], 'pi', free, 'a'), null)
  assert.equal(selectedAccount([account], 'pi', go, 'ctx'), null)
  assert.equal(selectedAccount([{ ...account, active: false }], 'pi', go, 'a'), null)
  assert.equal(selectedAccount([account, { ...account, id: 'b' }], 'pi', go), null)
  assert.equal(selectedAccount([{ ...account, routeId: 'opencode-go/other' }], 'pi', go), null)
})
test('new profiles use live harness defaults and preserve the selected exact model', () => {
  assert.equal(blankProfile().harness, 'claude-code')
  assert.deepEqual(blankProfile().compact, { tokens: 200000 })
  assert.deepEqual(blankProfile('pi').compact, { tokens: 200000 })
  assert.equal(blankProfile('other').compact, null)
  assert.equal(blankProfile('pi', go.id).model, go.id)
  assert.equal(blankProfile().name, '')
  assert.deepEqual(blankProfile().envKeys, [])
})
test('new profiles start every rating at neutral 5, including diagnostics', () => {
  const { taste, diagnostics, intelligence, cost } = blankProfile()
  assert.deepEqual({ taste, diagnostics, intelligence, cost }, { taste: 5, diagnostics: 5, intelligence: 5, cost: 5 })
})

test('single-route harnesses join by harness while Pi follows credential integration', () => {
  const claude = accountFixture({ id: 'claude-account', harness: 'claude-code', providerId: 'anthropic', active: true })
  assert.equal(selectedAccount([claude], 'claude-code', { id: 'sonnet', providerId: 'anthropic' }), claude)
  const consoleAccount = accountFixture({ id: 'console-account', harness: 'pi', providerId: 'opencode', active: true })
  const route = { id: 'opencode-go/model', providerId: 'opencode-go', integrationId: 'opencode' }
  assert.equal(selectedAccount([consoleAccount], 'pi', route), consoleAccount)
  assert.equal(selectedAccount([consoleAccount], 'pi', { ...route, integrationId: 'different' }), null)
})

test('publisher filters preserve exact harness routes', () => {
  assert.deepEqual(filterModels(models, { provider: 'Publisher' }), [vendor])
  assert.equal(need(filterModels(models, { provider: 'Publisher' })[0]).id, 'vendor/model')
})

test('normalizeModels trims, drops blanks and keeps the first of each duplicate', () => {
  assert.deepEqual(normalizeModels('a', [' a ', '', 'b', 'a', 'c']), ['a', 'b', 'c'])
  assert.deepEqual(normalizeModels('x', ['y']), ['x'])
  assert.deepEqual(normalizeModels(' solo '), ['solo'])
  assert.deepEqual(normalizeModels('', []), [])
  assert.deepEqual(normalizeModels('x', ['', '  ']), ['x'])
  assert.deepEqual(normalizeModels(undefined, undefined), [])
})
test('blankProfile carries a model chain', () => {
  const p = blankProfile('opencode', 'ignored', ['a', 'b', 'a'])
  assert.equal(p.model, 'a')
  assert.deepEqual(p.models, ['a', 'b'])
  assert.deepEqual(blankProfile('opencode', 'm').models, ['m'])
  assert.deepEqual(blankProfile().models, [])
  assert.equal(blankProfile('opencode', 'm', []).model, 'm')
})
test('effort choice replaces earlier effort args and keeps other args', () => {
  const high = effort('high', { args: ['--reasoning-effort', 'high'] }), low = effort('low', { args: ['--reasoning-effort', 'low'] })
  const efforts = [high, low]
  const args = ['--no-plan', '--reasoning-effort', 'high', '--max-turns', '3']
  assert.deepEqual(withEffort(args, efforts, low), ['--no-plan', '--max-turns', '3', '--reasoning-effort', 'low'])
  assert.deepEqual(withEffort(args, efforts, null), ['--no-plan', '--max-turns', '3'])
  assert.deepEqual(withEffort(['--x'], undefined, low), ['--x', '--reasoning-effort', 'low'])
  assert.equal(effortIn(args, efforts), 'high')
  assert.equal(effortIn(['--reasoning-effort'], efforts), '')
})
test('model-ID efforts route to their group and add no args', () => {
  const low = effort('low', { model: 'gemini-4-argon-low' })
  const group = model('gemini-4-argon-medium', { efforts: [low, effort('medium', { default: true, model: 'gemini-4-argon-medium' })] })
  assert.deepEqual(routeEffort([group], 'gemini-4-argon-low'), { model: group, effort: 'low' })
  assert.equal(routeEffort([group], 'other'), null)
  assert.equal(routeModel([group], 'gemini-4-argon-low'), null)
  assert.deepEqual(withEffort(['--x'], group.efforts, low), ['--x'])
})

test('the editor finds the effort a saved profile selects, by args or by model ID', () => {
  const high = effort('high', { args: ['--effort', 'high'] })
  const haiku = model('haiku', { efforts: [effort('low', { args: ['--effort', 'low'] }), high] })
  assert.deepEqual(profileEffort([haiku], 'haiku', ['--x', '--effort', 'high']), { model: haiku, effort: 'high' })
  assert.deepEqual(profileEffort([haiku], 'manual-id', []), { model: null, effort: '' })
  const gem = model('gem-medium', { efforts: [effort('low', { model: 'gem-low' }), effort('medium', { model: 'gem-medium' })] })
  assert.deepEqual(profileEffort([gem], 'gem-low', []), { model: gem, effort: 'low' })
})

test('choosing an effort changes args, or the model ID for model-ID efforts', () => {
  const claude = model('haiku', { efforts: [effort('low', { args: ['--effort', 'low'] }), effort('high', { args: ['--effort', 'high'] })] })
  assert.deepEqual(chooseEffort(claude, 'low', 'haiku', ['--x', '--effort', 'high']), { model: 'haiku', args: ['--x', '--effort', 'low'] })
  assert.deepEqual(chooseEffort(claude, '', 'haiku', ['--effort', 'high']), { model: 'haiku', args: [] })
  const ids = model('gem-medium', { efforts: [effort('low', { model: 'gem-low' }), effort('medium', { model: 'gem-medium' })] })
  assert.deepEqual(chooseEffort(ids, 'low', 'gem-medium', ['--x']), { model: 'gem-low', args: ['--x'] })
  assert.deepEqual(chooseEffort(ids, '', 'gem-low', ['--x']), { model: 'gem-medium', args: ['--x'] })
})

test('compact needs a harness threshold and, for relative ones, every chain model window', () => {
  const absolute = { min: 1 }, muse = { window: true, min: 1 }
  const sized = [model('big', { context: 1000000 }), model('small', { context: 200000 })]
  const windows = compactWindows(sized, ['big', 'small'])
  assert.deepEqual(windows, { big: 1000000, small: 200000 })
  assert.equal(compactBlock({ reason: 'Cursor Agent has no compaction threshold setting.' }, {}), 'Cursor Agent has no compaction threshold setting.')
  assert.equal(compactBlock(absolute, { '': 0 }), '')
  assert.match(compactBlock(muse, compactWindows(sized, ['big', 'manual'])), /none for manual/)
  assert.match(compactBlock(muse, compactWindows(sized, [])), /harness default model/)
  assert.deepEqual(compactFor(absolute, { '': 0 }, 95000), { tokens: 95000 })
  assert.deepEqual(compactFor(muse, windows, 95000), { tokens: 95000, windows })
  // A saved window stays when the catalog does not list the model.
  assert.deepEqual(compactWindows(sized, ['big', 'manual'], { tokens: 1, windows: { manual: 150000 } }), { big: 1000000, manual: 150000 })
  assert.equal(compactFor(absolute, {}, 0), null)
  assert.equal(compactFor({ reason: 'none' }, {}, 95000), null)
})

test('the compact hint names Dyna\'s compact command for trigger harnesses', () => {
  assert.equal(compactHint({ reason: 'x' }, 'x'), 'x')
  assert.equal(compactHint({ min: 67000 }, '', 967000), 'Leave blank for the harness default. From 67,000 to 967,000.')
  assert.match(compactHint({ trigger: true, min: 1 }), /Dyna sends its compact command before the next turn/)
  assert.doesNotMatch(compactHint({ min: 1 }), /compact command/)
  assert.match(compactHint({ min: 1 }, '', 0, 'pi'), /share a model use the smallest target/)
})
