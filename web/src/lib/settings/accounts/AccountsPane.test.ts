import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../../prs/prDetailHarness.ts'
import { account } from '../../../../test/account-fixtures.ts'
import { element, elements, need, textOf } from '../../../../test/need.ts'
import type { AccountsSnapshot, ScopeAccount } from '../../api/account.ts'

// The page lists the saved accounts by provider, and its Use button chooses an
// account for the scope the owner shows: global, or this room.
const api = `const view = (scope, refresh) => refresh && globalThis.accountsRefreshImpl
  ? globalThis.accountsRefreshImpl(scope)
  : globalThis.accountsViewImpl ? globalThis.accountsViewImpl(scope) : Promise.resolve(globalThis.accountsPage(scope))
export const api = {
  accountsView: (scope) => view(scope, false),
  accountsList: (scope, refresh) => view(scope, refresh),
  accountsUse: (id, scope) => { globalThis.accountsCalls.push([id, scope]); return globalThis.accountsUseImpl ? globalThis.accountsUseImpl(id, scope) : Promise.resolve(globalThis.accountsPage(scope)) },
  setAccountRepos: () => Promise.resolve({}),
  accountsRemove: () => Promise.resolve(globalThis.accountsPage('')),
  accountsAdd: () => Promise.resolve(globalThis.accountsPage('')),
}`

const built = await bundle({ component: '../settings/accounts/AccountsPane', api })

const row = (fields: Partial<ScopeAccount> & Pick<ScopeAccount, 'id' | 'provider'>): ScopeAccount => ({
  ...account({ id: fields.id, harness: 'pi' }),
  label: fields.id,
  activeHere: false,
  ...fields,
})

// settle waits for the pane's load promise and the render it causes.
async function settle(flushSync: () => void) {
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
  flushSync()
}

test('accounts are grouped by provider and Use calls the API with the selected scope', async () => {
  const work = row({ id: 'claude-work', provider: 'anthropic', label: 'Claude · Work', active: true, activeHere: true })
  const home = row({ id: 'claude-home', provider: 'anthropic', label: 'Claude · Personal' })
  const zai = row({ id: 'zai-key', provider: 'zai', kind: 'api_key', label: 'Z.ai key' })
  const calls: [string, string][] = []
  // The saved accounts in order, and the account the last Use chose in a scope.
  const saved: AccountsSnapshot = { providers: [{ provider: 'anthropic', accounts: [work, home] }, { provider: 'zai', accounts: [zai] }], problems: [], checkedAt: '2026-10-25T12:00:00Z' }
  const groups = saved.providers ?? []
  let inRoom: Record<string, string> = {}
  const dom = installDom({
    accountsCalls: calls,
    accountsPage: (scope: string) => ({
      ...saved,
      scope,
      providers: groups.map((group) => ({ ...group, accounts: (group.accounts ?? []).map((a) => ({ ...a, activeHere: scope ? inRoom[scope] === a.id : a.active })) })),
    }),
  })
  const { dir, default: AccountsPane, mount, flushSync, unmount } = await importBundle(built, 'AccountsPane')
  let mounted: ReturnType<typeof mount> | undefined
  const rows = (target: HTMLElement) => [...target.querySelectorAll('.account')]
  const useButtons = (target: HTMLElement) => elements<HTMLButtonElement>(target, '.account button').filter((b) => textOf(b) === 'Use')
  const useFor = (target: HTMLElement, label: string) => need(useButtons(target).find((b) => textOf(b.closest('.account')).includes(label)), `Use button for ${label}`)
  try {
    const target = need(document.getElementById('app'), 'app')
    mounted = mount(AccountsPane, { target, props: { roomId: 'room-a', onmodal: () => {} } })
    await settle(flushSync)

    assert.deepEqual([...target.querySelectorAll('h4')].map(textOf), ['Anthropic', 'Z.ai'], 'one group per provider, in engine order')
    assert.deepEqual(rows(target).map((r) => textOf(need(r.querySelector('b'), 'account label'))), ['Claude · Work', 'Claude · Personal', 'Z.ai key'])
    assert.match(textOf(need(rows(target)[2], 'zai row')), /Usage unavailable for an API key/, 'a key account names why it has no meters')

    // The account its provider uses in this scope shows a filled marker instead.
    assert.match(textOf(need(rows(target)[0], 'work row')), /In use/)
    assert.equal(useButtons(target).length, 2, 'only an account that is not in use offers Use')

    useFor(target, 'Claude · Personal').click()
    await settle(flushSync)
    assert.deepEqual(calls, [['claude-home', '']], 'the global scope is the engine default')

    element(target, '[role="group"][aria-label="Account scope"] button:nth-of-type(2)').click()
    await settle(flushSync)
    inRoom = { 'room-a': 'claude-home' }
    useFor(target, 'Z.ai key').click()
    await settle(flushSync)
    assert.deepEqual(calls.at(-1), ['zai-key', 'room-a'], 'the room scope names the room')
    assert.match(textOf(need(rows(target)[0], 'work row')), /Global choice/, 'the global account is shown as inherited')
  } finally {
    if (mounted) await unmount(mounted)
    dom.window.close()
    for (const name of [...domNames, 'accountsCalls', 'accountsPage']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})

test('a delayed switch for the old scope cannot replace the room view', async () => {
  const globalRow = row({ id: 'global-account', provider: 'anthropic', label: 'Global work', active: true, activeHere: true })
  const spare = row({ id: 'spare', provider: 'anthropic', label: 'Spare account' })
  const snapshotOf = (accounts: ScopeAccount[]): AccountsSnapshot => ({ providers: [{ provider: 'anthropic', accounts }], problems: [], checkedAt: '2026-10-25T12:00:00Z' })
  const globalView = snapshotOf([globalRow, spare])
  const lateGlobal = snapshotOf([{ ...globalRow, label: 'Late global answer' }])
  const roomView = snapshotOf([{ ...spare, label: 'Room choice', activeHere: true }])
  const useAnswer = Promise.withResolvers<AccountsSnapshot>()
  const roomAnswer = Promise.withResolvers<AccountsSnapshot>()
  let roomReads = 0
  const dom = installDom({
    accountsCalls: [],
    accountsPage: () => globalView,
    accountsViewImpl: (scope: string) => {
      if (!scope) return Promise.resolve(globalView)
      roomReads++
      return roomReads === 1 ? roomAnswer.promise : Promise.resolve(roomView)
    },
    accountsUseImpl: () => useAnswer.promise,
  })
  const { dir, default: AccountsPane, mount, flushSync, unmount } = await importBundle(built, 'AccountsPane')
  let mounted: ReturnType<typeof mount> | undefined
  const settle = async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); flushSync() }
  try {
    const target = need(document.getElementById('app'), 'app')
    mounted = mount(AccountsPane, { target, props: { roomId: 'room-a', onmodal: () => {} } })
    await settle()
    const useButton = elements<HTMLButtonElement>(target, '.account button').find((b) => textOf(b) === 'Use')
    need(useButton, 'Use button').click()
    await settle()
    element(target, '[role="group"][aria-label="Account scope"] button:nth-of-type(2)').click()
    await settle()
    roomAnswer.resolve(roomView)
    await settle()
    assert.match(textOf(target), /Room choice/, 'the room scope is showing')
    useAnswer.resolve(lateGlobal)
    await settle()
    assert.match(textOf(target), /Room choice/, 'the room view stays after the late global answer')
    assert.doesNotMatch(textOf(target), /Late global answer/)
    assert.equal(roomReads, 2, 'the late answer reads the current scope again')
  } finally {
    if (mounted) await unmount(mounted)
    dom.window.close()
    for (const name of [...domNames, 'accountsCalls', 'accountsPage', 'accountsViewImpl', 'accountsUseImpl']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})

// D-148: while the providers are asked, the page names the wait and keeps the
// saved readings on screen, and the wait ends when the answer arrives.
test('a refresh names the wait, keeps the saved readings, and ends with the answer', async () => {
  const work = row({ id: 'claude-work', provider: 'anthropic', label: 'Claude · Work', active: true, observedAt: '2026-10-25T11:00:00Z', stale: true })
  const saved: AccountsSnapshot = { providers: [{ provider: 'anthropic', accounts: [work] }], problems: [], checkedAt: '2026-10-25T12:00:00Z' }
  const answer = Promise.withResolvers<AccountsSnapshot>()
  const dom = installDom({ accountsCalls: [], accountsPage: () => saved, accountsRefreshImpl: () => answer.promise })
  const { dir, default: AccountsPane, mount, flushSync, unmount } = await importBundle(built, 'AccountsPane')
  let mounted: ReturnType<typeof mount> | undefined
  try {
    const target = need(document.getElementById('app'), 'app')
    mounted = mount(AccountsPane, { target, props: { onmodal: () => {} } })
    await settle(flushSync)
    assert.match(elements<HTMLButtonElement>(target, 'button').map(textOf).join(' '), /Refresh/)
    need(elements<HTMLButtonElement>(target, 'button').find((b) => textOf(b) === 'Refresh'), 'Refresh button').click()
    await settle(flushSync)
    assert.match(textOf(target), /Asking each provider for current usage/, 'the page names the wait')
    assert.match(textOf(target), /Claude · Work/, 'the saved readings stay while the providers answer')
    assert.match(elements<HTMLButtonElement>(target, 'button').map(textOf).join(' '), /Refreshing…/)
    answer.resolve({ ...saved, checkedAt: '2026-10-25T12:05:00Z' })
    await settle(flushSync)
    assert.doesNotMatch(textOf(target), /Asking each provider for current usage/, 'the wait ends with the answer')
    assert.doesNotMatch(elements<HTMLButtonElement>(target, 'button').map(textOf).join(' '), /Refreshing…/)
  } finally {
    if (mounted) await unmount(mounted)
    dom.window.close()
    for (const name of [...domNames, 'accountsCalls', 'accountsPage']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})
