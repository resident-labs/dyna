// Isolated browser fixture. This entry is never imported by the production app.
window.addEventListener('error', (e) => { document.body.dataset.error = e.message })
import { mount, unmount, tick } from 'svelte'
import Wizard from '../src/lib/settings/ProfileWizard.svelte'
import Editor from '../src/lib/settings/ProfileEditor.svelte'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { api } from '../src/api.ts'
import { blankProfile, type EditorDraft, type ModelSelection } from '../src/lib/settings/profiles/state.ts'
import type { CatalogModel } from '../src/lib/api/account.ts'
import type { ProfileView } from '../src/lib/api/profile.ts'
import type { HarnessSupport } from '../src/lib/settings/support.ts'

// noSupport is a harness Dyna cannot read yet. The pane shows it as unknown.
const noSupport: HarnessSupport = { installed: true, installable: true, binary: '', version: '', versionError: '', lastHealthyVersion: '', dialect: '', partial: false, status: 'unknown', reason: '', items: [] }
import type { ProfileSave } from '../src/lib/api/engine.ts'

// savedView is the saved profile as the pane reads it: the name it reports.
const savedView = (data: ProfileSave): ProfileView => ({ runtimeVersion: '', fallback: '', support: noSupport, ...data.profile })
import { account } from '../test/account-fixtures.ts'
import { element, need } from '../test/need.ts'

const query = new URLSearchParams(location.search)
const scenario = query.get('screen') || 'harness'
const models: CatalogModel[] = [
 { id: 'opencode-go/test-model', label: 'Fixture Go model', providerId: 'opencode-go', publisher: 'Fixture provider', category: 'go', context: 256000, input: 196000, output: 64000, source: 'Isolated UI fixture', description: 'An isolated fixture exercises model details. It does not represent a real model or account.', limits: [{ label: 'Published fixture allowance', value: 'Not a production allowance', source: 'UI fixture only' }] },
 { id: 'opencode/test-free', label: 'Fixture free model', providerId: 'opencode', publisher: 'Fixture provider', category: 'free', source: '', limits: null },
 { id: 'vendor/test-other', label: 'Fixture API model', providerId: 'vendor', publisher: 'Other provider', category: 'other', source: '', limits: null },
]
api.profileModels = async () => {
 if (scenario === 'error') throw new Error('Fixture discovery error')
 return { models: scenario === 'empty' ? [] : models, compact: { window: true }, source: 'Isolated UI fixture', observedAt: '2026-10-17T17:30:00Z' }
}
api.accountsList = async () => ({ checkedAt: '2026-10-17T17:30:00Z', problems: [], providers: [{ provider: 'opencode-go', accounts: [{ ...account({ id: 'fixture-account', harness: 'opencode', provider: 'opencode-go', providerId: 'opencode-go', active: true, label: 'Fixture account', status: 'authenticated', source: 'UI fixture only', observedAt: '2026-10-17T17:30:00Z', windows: [{ id: 'short', label: 'Fixture window', usedPercent: 20 }] }), activeHere: true }] }] })
// What the last save asked for.
const sent: { saved?: ProfileSave } = {}
api.saveProfile = async (data: ProfileSave) => { document.body.dataset.savedModel = data.profile.model; return savedView(data) }
const customScenarios = ['custom-error', 'draft-details', 'draft-navigation', 'advanced-details', 'advanced-navigation']
const initialSelection: ModelSelection | null = scenario === 'harness' ? null : { harness: customScenarios.includes(scenario) ? 'custom' : 'opencode', ...(['details', 'settings'].includes(scenario) ? { connectionId: 'fixture-account', routeId: need(models[0]).id } : {}) }
const app = need(document.getElementById('app'), 'app')
const wizard = mount(Wizard, { target: app, props: { harnesses: ['opencode', 'codex', 'custom'], initialSelection, onsaved: () => {}, oncancel: () => {}, onusage: (id: string) => { document.body.dataset.usage = id } } })

const results: { name: string; pass: boolean }[] = []
const pause = () => new Promise<void>((r) => setTimeout(r, 80))
const check = (name: string, condition: unknown) => results.push({ name, pass: !!condition })
const findButton = (text: string) => [...document.querySelectorAll('button')].find((el) => el.textContent?.includes(text))
const button = (text: string) => need(findButton(text), `button ${text}`)
const nav = (label: string) => need([...document.querySelectorAll<HTMLElement>('nav button')].find((el) => el.textContent?.includes(label)), `nav ${label}`)
const one = <T extends Element = HTMLElement>(selector: string) => element<T>(document, selector)
const setValue = async (el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string, event = 'input') => { el.value = value; el.dispatchEvent(new Event(event, { bubbles: true })); await tick() }
const input = (selector: string, value: string, event = 'input') => setValue(one<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(selector), value, event)
const text = () => document.body.textContent ?? ''
const report = () => {
 const pre = document.createElement('pre'); pre.id = 'smoke-results'; pre.textContent = JSON.stringify(results); document.body.append(pre)
 document.body.dataset.smoke = results.every((r) => r.pass) ? 'passed' : 'failed'
}

if (scenario === 'settings') { await new Promise((r) => setTimeout(r, 100)); button('Use this model').click() }
if (scenario === 'test') {
 await pause()
 check('OpenCode initial list includes Go and free only', document.querySelectorAll('.model-row').length === 2)
 button('All models').click(); await tick()
 check('All models reveals API model', document.querySelectorAll('.model-row').length === 3)
 await input('input[aria-label="Search models"]', 'Fixture API')
 check('Search filters discovered rows', document.querySelectorAll('.model-row').length === 1)
 button('Fixture API model').click(); await tick()
 check('Exact API model appears in details', text().includes('vendor/test-other'))
 button('Use this model').click(); await tick()
 check('Guided final step retains navigation', text().includes('Make it your own') && document.querySelector('nav [aria-current=step]')?.textContent?.includes('Profile'))
 check('Advanced options start folded', !document.querySelector<HTMLDetailsElement>('details.advanced')?.open)
 check('Editor receives exact model and blank name', document.querySelector<HTMLInputElement>('input[placeholder="the harness default"]')?.value === 'vendor/test-other' && document.querySelector<HTMLInputElement>('input[placeholder="e.g. quick-research"]')?.value === '')
 button('← Details').click(); await tick()
 check('Cancel settings returns to model detail', text().includes('Model overview'))
 button('← Back').click(); await tick()
 button('Enter a model ID').click(); await tick()
 await input('input[placeholder="Harness default"]', '')
 check('Manual default has explicit action', findButton('Use harness default'))
 button('Use harness default').click(); await tick()
 await input('input[placeholder="e.g. quick-research"]', 'fixture-created')
 button('Create profile').click(); await pause()
 check('Default is saved as an empty model ID', document.body.dataset.savedModel === '')
 report()
}

if (scenario === 'custom-error') {
 await pause(); button('Model details').click(); await tick(); button('Use harness default').click(); await tick()
 const name = one<HTMLInputElement>('input[placeholder="e.g. quick-research"]'), command = one<HTMLTextAreaElement>('textarea[placeholder="One argument per line"]')
 check('Custom command is visible without opening Advanced', command.getClientRects().length && !one<HTMLDetailsElement>('details.advanced').open)
 await setValue(name, 'custom-fixture'); await setValue(command, 'fixture-agent\n{{prompt}}')
 api.saveProfile = async () => { throw new Error('Fixture save failure: please retry. '.repeat(5)) }; button('Create profile').click(); await pause()
 const error = document.querySelector('[role=alert]')
 check('Save error is visible', !!error && error.getBoundingClientRect().top >= 0 && error.getBoundingClientRect().bottom <= innerHeight)
 check('Save error preserves name and command', name.value === 'custom-fixture' && command.value === 'fixture-agent\n{{prompt}}')
 check('Save action remains Create profile', findButton('Create profile') && !button('Create profile').disabled)
 api.saveProfile = async (data: ProfileSave) => { document.body.dataset.savedModel = data.profile.model; return savedView(data) }; button('Create profile').click(); await pause()
 check('Custom harness default remains empty on retry', document.body.dataset.savedModel === '')
 report()
}

if (['draft-details', 'draft-navigation', 'draft-environment'].includes(scenario)) {
 const command = 'fixture-agent\n{{prompt}}', args = '--fixture=true\n--second=value'
 const row = (key: string) => [...document.querySelectorAll('.envrow')].find((el) => el.textContent?.includes(key))
 const add = async (key: string, value: string) => { await input('input[placeholder="KEY"]', key); await input('input[placeholder="value"]', value); button('Add').click(); await tick() }
 api.saveProfile = async (data: ProfileSave) => { sent.saved = data; return savedView(data) }
 await pause()
 if (scenario !== 'draft-environment') {
  button('Model details').click(); await tick(); button('Use harness default').click(); await tick()
  await input('input[placeholder="e.g. quick-research"]', 'custom-fixture')
  await input('textarea[placeholder="One argument per line"]', command)
  one('details.advanced summary').click(); await pause()
  await input('textarea[placeholder="--flag=value"]', args)
  await add('FIXTURE_KEEP', 'fixture-value'); await add('FIXTURE_DROP', 'discard')
  need(row('FIXTURE_DROP')?.querySelector<HTMLElement>('.bad'), 'remove button').click(); await tick()
  await input('input[placeholder="KEY"]', 'FIXTURE_PENDING'); await input('input[placeholder="value"]', 'pending-value')
  if (scenario === 'draft-details') button('← Details').click()
  else nav('Model').click()
  await tick()
  if (scenario === 'draft-navigation') { button('Model details').click(); await tick() }
  button('Use harness default').click(); await tick()
  check('Profile name survives navigation', one<HTMLInputElement>('input[placeholder="e.g. quick-research"]').value === 'custom-fixture')
  check('Raw custom command survives navigation', one<HTMLTextAreaElement>('textarea[placeholder="One argument per line"]').value === command)
  check('Raw extra arguments survive navigation', one<HTMLTextAreaElement>('textarea[placeholder="--flag=value"]').value === args)
  check('Advanced section stays open', one<HTMLDetailsElement>('details.advanced').open)
  check('Staged environment addition survives', row('FIXTURE_KEEP')?.textContent?.includes('new value set'))
  check('Removed staged key stays absent', !row('FIXTURE_DROP'))
  check('Pending environment input survives', one<HTMLInputElement>('input[placeholder="KEY"]').value === 'FIXTURE_PENDING' && one<HTMLInputElement>('input[placeholder="value"]').value === 'pending-value')
  button('Add').click(); await tick(); button('Create profile').click(); await pause()
  check('Save receives command and arguments', JSON.stringify(sent.saved?.profile.command) === JSON.stringify(command.split('\n')) && JSON.stringify(sent.saved?.profile.extraArgs) === JSON.stringify(args.split('\n')))
  check('Save receives complete staged environment', sent.saved?.envSet?.FIXTURE_KEEP === 'fixture-value' && sent.saved.envSet.FIXTURE_PENDING === 'pending-value' && !('FIXTURE_DROP' in sent.saved.envSet))
 } else {
  // Existing removals need an editor with existing keys. All values are fixtures.
  await unmount(wizard)
  for (const path of ['details', 'navigation']) {
   const kept: { draft?: EditorDraft } = {}
   const props = { guided: true, profile: { ...blankProfile('custom'), name: 'fixture', command: ['fixture-agent'], envKeys: ['FIXTURE_REMOVE', 'FIXTURE_REPLACE'] }, harnesses: ['custom'], onsaved: () => {}, oncancel: (value: EditorDraft) => { kept.draft = value }, onback: (_step: number, value: EditorDraft) => { kept.draft = value } }
   let instance = mount(Editor, { target: app, props }); await tick()
   one('details.advanced summary').click(); await pause()
   need(row('FIXTURE_REMOVE')?.querySelector<HTMLElement>('.bad'), 'remove button').click(); await tick()
   need(row('FIXTURE_REPLACE')?.querySelector('button'), 'replace button').click(); await tick()
   await input('input[placeholder="new value, then Enter"]', 'pending-replacement')
   if (path === 'details') button('← Details').click(); else one('nav button').click()
   await tick(); await unmount(instance)
   const draft = need(kept.draft, 'draft')
   instance = mount(Editor, { target: app, props: { ...props, profile: draft.profile, draft } }); await tick()
   check(`${path}: existing removal survives`, row('FIXTURE_REMOVE')?.classList.contains('removed'))
   check(`${path}: pending replacement survives`, document.querySelector<HTMLInputElement>('input[placeholder="new value, then Enter"]')?.value === 'pending-replacement')
   one('input[placeholder="new value, then Enter"]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); await tick()
   button('Create profile').click(); await pause()
   check(`${path}: save receives removal and replacement`, sent.saved?.envRemove?.includes('FIXTURE_REMOVE') && sent.saved.envSet?.FIXTURE_REPLACE === 'pending-replacement')
   await unmount(instance)
  }
 }
 report()
}

if (['advanced-details', 'advanced-navigation'].includes(scenario)) {
 api.saveProfile = async (data: ProfileSave) => { sent.saved = data; return savedView(data) }
 await pause(); button('Model details').click(); await tick(); button('Use harness default').click(); await tick()
 await input('input[placeholder="e.g. quick-research"]', 'advanced-fixture')
 one('details.advanced summary').click(); await pause()
 await input('details.advanced select', 'opencode', 'change')
 await input('input[placeholder="the harness default"]', 'fixture-advanced-model')
 if (scenario === 'advanced-details') button('← Details').click(); else nav('Model').click()
 await tick(); await pause()
 if (scenario === 'advanced-navigation') { button('Enter a model ID').click(); await tick() }
 check('Earlier details show the Advanced harness', document.querySelector('.model-context')?.textContent?.includes('OpenCode'))
 check('Earlier details show the Advanced model', document.querySelector<HTMLInputElement>('input[placeholder="Harness default"]')?.value === 'fixture-advanced-model')
 button('Use this model').click(); await tick()
 check('Advanced model survives return', one<HTMLInputElement>('input[placeholder="the harness default"]').value === 'fixture-advanced-model')
 check('Advanced harness survives return', one<HTMLSelectElement>('details.advanced select').value === 'opencode')
 button('Create profile').click(); await pause()
 check('Final save preserves Advanced selection', sent.saved?.profile.harness === 'opencode' && sent.saved.profile.model === 'fixture-advanced-model')
 nav('Harness').click(); await tick(); need([...document.querySelectorAll<HTMLElement>('.harness')].find((el) => el.querySelector('strong')?.textContent === 'OpenCode'), 'OpenCode harness').click(); await tick(); button('Enter a model ID').click(); await tick()
 check('Revisiting the same harness preserves Advanced model', one<HTMLInputElement>('input[placeholder="Harness default"]').value === 'fixture-advanced-model')
 button('Use this model').click(); await tick()
 nav('Model').click(); await tick(); button('Fixture free model').click(); await tick(); button('Use this model').click(); await tick()
 button('Create profile').click(); await pause()
 check('Explicit picker model replaces Advanced selection', sent.saved?.profile.model === 'opencode/test-free')
 nav('Harness').click(); await tick(); button('Bring your own harness').click(); await tick(); button('Model details').click(); await tick(); button('Use harness default').click(); await tick()
 await input('textarea[placeholder="One argument per line"]', 'fixture-agent')
 button('Create profile').click(); await pause()
 check('Explicit picker harness replaces Advanced selection', sent.saved?.profile.harness === 'custom' && sent.saved.profile.model === '')
 report()
}
