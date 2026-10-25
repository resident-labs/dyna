import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../../prs/prDetailHarness.ts'
import { element, elements, need, textOf } from '../../../../test/need.ts'
import type { HarnessSnapshot } from '../../api/harness.ts'

// The add form must start a sign-in for a new account, not a sign-in again of
// an account that does not exist, and it must save a key once and clear it.
const api = `export const api = {
  privateRuntimes: () => Promise.resolve(globalThis.addRuntimes),
  privateRuntimeLogins: () => Promise.resolve(globalThis.addFlows ?? []),
  privateRuntimeLoginState: () => new Promise(() => {}),
  findPersonalLogin: (...args) => { globalThis.addCalls.push(['find', ...args]); return Promise.resolve(null) },
  beginPrivateRuntimeLogin: (...args) => { globalThis.addCalls.push(['begin', ...args]); return Promise.resolve({ accountId: 'new-account', signIn: { phase: 'waiting' } }) },
  restartPrivateRuntimeLogin: (...args) => { globalThis.addCalls.push(['restart', ...args]); return Promise.resolve({ accountId: 'saved', signIn: { phase: 'waiting' } }) },
  accountsAdd: (...args) => { globalThis.addCalls.push(['add', ...args]); return Promise.resolve(globalThis.addSnapshot) },
  accountsView: () => Promise.resolve(globalThis.addSnapshot),
}`

const built = await bundle({ component: '../settings/accounts/AddAccount', api })

const runtimes: HarnessSnapshot = {
  bundles: [
    { harness: 'claude-code', version: '2.1.302', executable: '/dyna/claude', directory: '/dyna/claude' },
    { harness: 'codex', version: '1.2.3', executable: '/dyna/codex', directory: '/dyna/codex' },
  ],
  accounts: [],
  profiles: [],
  capabilities: [],
  installing: [],
  selectedVersions: {},
  recommendedVersions: [],
}
const snapshot = { providers: [], problems: [], checkedAt: '2026-10-25T12:00:00Z' }

type Mounted = { target: HTMLElement; unmount: (app: object) => Promise<void>; flushSync: () => void; app: object }
async function mountForm(t: { after: (fn: () => Promise<void>) => void }, dom: ReturnType<typeof installDom>, account: { id: string; label: string; provider: string; harness: string } | null = null): Promise<Mounted> {
  const { dir, default: AddAccount, mount, flushSync, unmount } = await importBundle(built, 'AddAccount')
  t.after(async () => {
    dom.window.close()
    for (const name of [...domNames, 'addCalls', 'addRuntimes', 'addSnapshot', 'addFlows']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  })
  const target = need(document.getElementById('app'), 'app')
  const app = mount(AddAccount, { target, props: { scope: '', account, onclose: () => {}, onsaved: () => {} } })
  await Promise.resolve(); await Promise.resolve(); flushSync()
  return { target, unmount, flushSync, app }
}
const settle = async (flushSync: () => void) => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); flushSync() }
const submit = (target: HTMLElement) => element(target, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
const selectHarness = (target: HTMLElement, value: string) => {
  const select = element<HTMLSelectElement>(target, 'select[aria-label="Harness"]')
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
}
const chooseMethod = (target: HTMLElement, value: 'oauth' | 'key') => element<HTMLInputElement>(target, `input[name="pi-method"][value="${value}"]`).click()
const selectProvider = (target: HTMLElement, value: string) => {
  const select = element<HTMLSelectElement>(target, 'select[aria-label="Provider"]')
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
}
const type = (target: HTMLElement, selector: string, value: string) => {
  const input = element<HTMLInputElement>(target, selector)
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

test('one provider choice starts that Pi OAuth flow', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    selectHarness(target, 'pi')
    flushSync()
    chooseMethod(target, 'oauth')
    selectProvider(target, 'kimi-code')
    flushSync()
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['begin', 'pi', '', '', 'kimi-code', false]], 'the chosen provider is sent once, so Pi does not ask again')
  } finally { await unmount(app) }
})

test('the form waits for a harness before it starts', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [], 'no sign-in starts before the harness choice')
  } finally { await unmount(app) }
})

test('the native path offers a saved login and starts Claude Code', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    const options = [...element<HTMLSelectElement>(target, 'select[aria-label="Harness"]').options].map((option) => option.value)
    assert.deepEqual(options, ['', 'claude-code', 'codex', 'muse', 'opencode', 'pi'], 'the other native harnesses keep their sign-in path')
    selectHarness(target, 'claude-code')
    flushSync()
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['find', 'claude-code', '2.1.302'], ['begin', 'claude-code', '2.1.302', '', '', false]])
  } finally { await unmount(app) }
})

test('a Codex native choice starts the Codex sign-in', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    selectHarness(target, 'codex')
    flushSync()
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['find', 'codex', '1.2.3'], ['begin', 'codex', '1.2.3', '', '', false]], 'every native harness starts its own sign-in')
  } finally { await unmount(app) }
})

test('Pi offers an API key for every supported provider', async (t) => {
  const dom = installDom({ addCalls: [], addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    selectHarness(target, 'pi')
    flushSync()
    chooseMethod(target, 'key')
    flushSync()
    const options = [...element<HTMLSelectElement>(target, 'select[aria-label="Provider"]').options].map((option) => option.value)
    assert.deepEqual(options, ['anthropic', 'chatgpt', 'kimi-code', 'xai', 'meta-muse', 'opencode-go', 'deepseek', 'zai'])
  } finally { await unmount(app) }
})

test('an API key is sent once and cleared from the form', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    selectHarness(target, 'pi')
    flushSync()
    chooseMethod(target, 'key')
    flushSync()
    selectProvider(target, 'deepseek')
    type(target, 'input[aria-label="Account label"]', 'Work')
    type(target, 'input[aria-label="API key"]', 'sk-test')
    flushSync()
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['add', 'pi', 'deepseek', 'Work', 'sk-test']])
    assert.equal(element<HTMLInputElement>(target, 'input[aria-label="API key"]').value, '', 'the key is cleared after submit')
    assert.doesNotMatch(target.textContent ?? '', /sk-test/)
  } finally { await unmount(app) }
})

test('an existing account signs in again', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom, { id: 'saved-account', label: 'Work', provider: 'anthropic', harness: 'claude-code' })
  try {
    assert.match(textOf(target), /Sign in again to Work/)
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['restart', 'saved-account', false]], 'a saved account restarts, not starts')
  } finally { await unmount(app) }
})

test('a reopened dialog shows the native sign-in saved for its account', async (t) => {
  const flows = [{ accountId: 'saved-account', harness: 'claude-code', signIn: { phase: 'waiting', acceptsPaste: true, message: 'Paste the code from the browser.' } }]
  const dom = installDom({ addCalls: [], addRuntimes: runtimes, addSnapshot: snapshot, addFlows: flows })
  const { target, unmount, flushSync, app } = await mountForm(t, dom, { id: 'saved-account', label: 'Work', provider: 'anthropic', harness: 'claude-code' })
  try {
    await settle(flushSync)
    assert.match(textOf(target), /Paste the code from the browser/, 'the saved step and its paste control are back')
    assert.ok(elements(target, 'button').some((b) => textOf(b) === 'Cancel sign-in'), 'the saved sign-in can still be cancelled')
  } finally { await unmount(app) }
})

test('an OpenCode Go key creates an OpenCode account', async (t) => {
  const calls: unknown[][] = []
  const dom = installDom({ addCalls: calls, addRuntimes: runtimes, addSnapshot: snapshot })
  const { target, unmount, flushSync, app } = await mountForm(t, dom)
  try {
    selectHarness(target, 'pi')
    flushSync()
    chooseMethod(target, 'key')
    flushSync()
    selectProvider(target, 'opencode-go')
    flushSync()
    const where = element<HTMLSelectElement>(target, 'select[aria-label="Save the key in"]')
    where.value = 'opencode'
    where.dispatchEvent(new Event('change', { bubbles: true }))
    type(target, 'input[aria-label="Account label"]', 'Go work')
    type(target, 'input[aria-label="API key"]', 'sk-go')
    flushSync()
    submit(target)
    await settle(flushSync)
    assert.deepEqual(calls, [['add', 'opencode', 'opencode-go', 'Go work', 'sk-go']], 'the key creates an OpenCode account, not a Pi one')
  } finally { await unmount(app) }
})

test('a reopened Pi sign-in finds the account it was started for', async (t) => {
  const flows = [{ accountId: 'pi:provider', harness: 'pi', target: 'pi-account', signIn: { phase: 'waiting', message: 'Approve the sign-in in your browser.' } }]
  const dom = installDom({ addCalls: [], addRuntimes: runtimes, addSnapshot: snapshot, addFlows: flows })
  const { target, unmount, flushSync, app } = await mountForm(t, dom, { id: 'pi-account', label: 'Work', provider: 'anthropic', harness: 'pi' })
  try {
    await settle(flushSync)
    assert.match(textOf(target), /Approve the sign-in in your browser/, 'the Pi card is recovered by target')
  } finally { await unmount(app) }
})
