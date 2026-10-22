// Actual component with fake account data. No backend or native account is used.
import '../src/app.css'
import { mount, unmount, tick } from 'svelte'
import AccountDetails from '../src/lib/settings/usage/AccountDetails.svelte'
import type { UsageAccount } from '../src/lib/api/account.ts'
import type { ModelSelection } from '../src/lib/settings/profiles/state.ts'
import { need } from '../test/need.ts'

const results: { name: string; pass: boolean }[] = [], selections: ModelSelection[] = []
const check = (name: string, value: unknown) => results.push({ name, pass: !!value })
const base: UsageAccount = { id: 'private-record', harness: 'opencode', routeId: 'opencode-go/fixture', label: 'Fixture account', status: 'ready', quotaBlocked: false, providerId: '', active: false, source: '', observedAt: '', stale: false, profiles: [], windows: [], needsLogin: false }
const cases: [string, Partial<UsageAccount>][] = [
 ['Inactive private', {}],
 ['Active private', { active: true }],
]
const app = need(document.getElementById('app'), 'app')
for (const [name, extra] of cases) {
 const account = { ...base, ...extra }, before = selections.length
 const instance = mount(AccountDetails, { target: app, props: { account, onclose: () => {}, oncreateprofile: (selection: ModelSelection) => selections.push(selection) } })
 await tick()
 const button = need([...document.querySelectorAll('button')].find((el) => el.textContent === 'Create profile'), 'Create profile')
 check(`${name}: creation follows active state`, button.disabled === !account.active)
 if (!account.active) check(`${name}: explains how to continue`, document.getElementById(button.getAttribute('aria-describedby') ?? '')?.textContent === 'Select this account before creating a profile.')
 button.click(); await tick()
 if (!account.active) check(`${name}: no profile handoff`, selections.length === before)
 else {
  const selection = need(selections.at(-1), 'selection')
  check(`${name}: exact harness and route survive`, selection.harness === base.harness && selection.routeId === base.routeId)
  check(`${name}: connection names the private account`, selection.connectionId === account.id)
 }
 await unmount(instance)
}
const report = document.createElement('pre'); report.id = 'smoke-results'; report.textContent = JSON.stringify(results); document.body.append(report)
document.body.dataset.smoke = results.every((result) => result.pass) ? 'passed' : 'failed'
