// Browser fixture only. This entry is not imported by the production app.
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import { mount } from 'svelte'
import Settings from '../src/lib/settings/Settings.svelte'
import { fixtureWindow, rollupFixture } from './usageRollupFixture.ts'
import { account } from '../test/account-fixtures.ts'
import type { ScopeAccount } from '../src/lib/api/account.ts'
import type { ModelSelection } from '../src/lib/settings/profiles/state.ts'
import { need } from '../test/need.ts'

const now = new Date().toISOString()
const windowFor = (id: string, label: string, used: number) => fixtureWindow(id, label, used, '2026-10-21T14:00:00Z')
const row = (fields: Parameters<typeof account>[0]): ScopeAccount => ({ ...account({ observedAt: now, ...fields }), activeHere: fields.active ?? false, servesHere: true })
const savedAccounts: ScopeAccount[] = [
  row({ id: 'claude-personal', harness: 'pi', provider: 'anthropic', label: 'Claude · Personal', active: true, plan: { id: 'max', label: 'Max 5x' }, source: 'Claude account report', profiles: ['reviewer'], windows: [windowFor('short', '5-hour window', 32), windowFor('weekly', 'Weekly window', 59)] }),
  row({ id: 'claude-work', harness: 'pi', provider: 'anthropic', label: 'Claude · Work', plan: { id: 'max', label: 'Max 20x' }, source: 'Claude account report', windows: [windowFor('short', '5-hour window', 26), windowFor('weekly', 'Weekly window', 37)] }),
  row({ id: 'go', harness: 'pi', provider: 'opencode-go', label: 'OpenCode Go · Personal', active: true, source: 'OpenCode Console usage', profiles: ['builder'], windows: [{ ...windowFor('short', '5-hour window', 20), used: 2.4, limit: 12, unit: 'USD' }, { ...windowFor('weekly', 'Weekly window', 30), used: 9, limit: 30, unit: 'USD' }, { id: 'month', label: 'Monthly window', usedPercent: 35, used: 21, limit: 60, unit: 'USD' }] }),
  row({ id: 'chatgpt', harness: 'pi', provider: 'chatgpt', label: 'ChatGPT · Personal', active: true, plan: { id: 'plus', label: 'Plus' }, source: 'ChatGPT account report', stale: true }),
]
const fixture: { selection?: ModelSelection } = {}
Reflect.set(window, 'usageFixture', fixture)
// The account each provider uses in each scope: '' is the global choice.
const chosen: Record<string, string> = {}
const grouped = (scope = '') => {
  const providers = [...new Set(savedAccounts.map((a) => a.provider ?? ''))]
  return providers.map((provider) => {
    const rows = savedAccounts.filter((a) => (a.provider ?? '') === provider)
    const pick = chosen[(scope || 'global') + ':' + provider] ?? rows.find((a) => a.activeHere)?.id ?? ''
    return { provider, accounts: rows.map((a) => ({ ...a, activeHere: a.id === pick })) }
  })
}
const snapshot = (scope = '') => ({ providers: grouped(scope), problems: [], checkedAt: now })
Reflect.set(window, 'go', { main: { App: {
  AccountsView: async (scope: string) => snapshot(scope),
  AccountsList: async (scope: string) => snapshot(scope),
  AccountsUse: async (id: string, scope: string) => {
    const picked = savedAccounts.find((a) => a.id === id)
    if (picked) chosen[(scope || 'global') + ':' + (picked.provider ?? '')] = id
    return snapshot(scope)
  },
  AccountsAdd: async () => ({ providers: grouped(), problems: [], checkedAt: now }),
  AccountsRemove: async () => ({ providers: grouped(), problems: [], checkedAt: now }),
  UsageRollup: async (_room: string, from: string, to: string) => rollupFixture(Math.max(1, Math.round((Date.parse(to) - Date.parse(from)) / 864e5) + 1)),
  Rooms: async () => [{ id: 'room-a', name: 'dyna-agent' }],
} } })
mount(Settings, { target: need(document.getElementById('app'), 'app'), props: { start: 'accounts', roomId: 'room-a', onclose: () => {}, oncreateprofile: (selection: ModelSelection) => { fixture.selection = selection } } })
