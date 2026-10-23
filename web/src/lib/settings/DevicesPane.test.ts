import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { domNames, installDom, bundle, importBundle } from '../prs/prDetailHarness.ts'
import { element, elements, need, textOf } from '../../../test/need.ts'

// DevicesPane with a deferred setDeviceScopes. The test settles each save.
const API = `export const api = {
  devices: () => globalThis.deviceReads ? new Promise((ok, fail) => globalThis.deviceReads.push({ ok, fail })) : Promise.resolve(structuredClone(globalThis.deviceRows)),
  setDeviceScopes: (id, scopes) => new Promise((ok, fail) => globalThis.scopeSaves.push({ id, scopes, ok, fail })),
}`
const built = await bundle({ component: '../settings/DevicesPane', api: API })
const settle = async () => { await Promise.resolve(); await Promise.resolve() }

function row(target: HTMLElement, name: string): HTMLElement {
  return need(elements(target, '.item').find((el) => textOf(el.querySelector('.name')).includes(name)), `row ${name}`)
}
function box(item: HTMLElement, name: string): HTMLInputElement {
  return need(elements<HTMLInputElement>(item, '.scopes input').find((el) => textOf(el.parentElement).trim() === name), `box ${name}`)
}
function saveButton(item: HTMLElement): HTMLButtonElement {
  return need(elements<HTMLButtonElement>(item, 'button').find((el) => textOf(el).trim() === 'Save'), 'Save')
}
const click = (el: HTMLElement) => el.click()
const toggle = (el: HTMLInputElement, flushSync: () => void) => {
  el.checked = !el.checked
  el.dispatchEvent(new window.Event('change', { bubbles: true }))
  flushSync()
}

interface Pane {
  target: HTMLElement
  flushSync: () => void
}
// The scope saves the page has asked for; the test settles each one.
interface ScopeSave {
  id: string
  scopes: string[]
  ok: (value: object) => void
  fail: (error: Error) => void
}
const saves = (): ScopeSave[] => Reflect.get(globalThis, 'scopeSaves')

async function pane(run: (pane: Pane) => Promise<void>, deferReads = false) {
  const dom = installDom({
    deviceRows: [
      { kind: 'paired', id: 'a', name: 'Phone A', scopes: ['read', 'operate'], scopeLabel: 'read, operate', lastSeen: '' },
      { kind: 'paired', id: 'b', name: 'Phone B', scopes: ['read'], scopeLabel: 'read', lastSeen: '' },
    ],
    scopeSaves: [],
    deviceReads: deferReads ? [] : null,
  })
  const { dir, default: DevicesPane, mount, flushSync } = await importBundle(built, 'DevicesPane')
  try {
    const target = need(document.getElementById('app'), 'app')
    mount(DevicesPane, { target, props: {} })
    await settle(); flushSync()
    await run({ target, flushSync })
  } finally {
    dom.window.close()
    for (const n of [...domNames, 'deviceRows', 'scopeSaves', 'deviceReads']) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
}

test('a save for one device does not close edits started on another', async () => {
  await pane(async ({ target, flushSync }) => {
    click(element(target, '[aria-label="Change scopes for Phone A"]'))
    flushSync()
    click(saveButton(row(target, 'Phone A')))
    flushSync()
    assert.equal(saves().length, 1)
    assert.equal(need(saves()[0]).id, 'a')

    click(element(target, '[aria-label="Change scopes for Phone B"]'))
    flushSync()
    const phoneB = row(target, 'Phone B')
    toggle(box(phoneB, 'files'), flushSync)
    assert.equal(box(phoneB, 'files').checked, true)

    need(saves()[0]).ok({})
    await settle(); await settle(); flushSync()
    assert.equal(box(row(target, 'Phone B'), 'files').checked, true, 'Phone B keeps the checkbox edit')
    assert.equal(target.textContent.includes('scope refused'), false)
    assert.equal(saveButton(row(target, 'Phone B')).disabled, false)
  })
})

test('a failed save does not clear a later save on another device', async () => {
  await pane(async ({ target, flushSync }) => {
    click(element(target, '[aria-label="Change scopes for Phone A"]'))
    flushSync()
    click(saveButton(row(target, 'Phone A')))
    flushSync()

    click(element(target, '[aria-label="Change scopes for Phone B"]'))
    flushSync()
    const phoneB = row(target, 'Phone B')
    toggle(box(phoneB, 'files'), flushSync)
    click(saveButton(phoneB))
    flushSync()
    assert.equal(need(saves()[1]).id, 'b')
    assert.deepEqual(need(saves()[1]).scopes, ['read', 'files'])

    need(saves()[0]).fail(new Error('scope refused'))
    await settle(); await settle(); flushSync()
    const still = row(target, 'Phone B')
    assert.equal(saveButton(still).disabled, true, 'Phone B stays busy')
    assert.equal(box(still, 'files').checked, true)
    assert.equal(target.textContent.includes('scope refused'), false, 'Phone A error stays off Phone B')
  })
})


test('device reads retry failures and keep loaded rows during a single refresh', async () => {
  await pane(async ({ target, flushSync }) => {
    const reads: { ok: (rows: object[]) => void; fail: (error: Error) => void }[] = Reflect.get(globalThis, 'deviceReads')
    const button = (name: string) => need(elements<HTMLButtonElement>(target, 'button').find((el) => textOf(el).trim() === name), name)
    need(reads.shift()).fail(new Error('device read failed'))
    await settle(); flushSync()
    button('Retry').click()
    flushSync()
    assert.equal(reads.length, 1)
    const rows = [
      { kind: 'paired', id: 'a', name: 'Phone A', scopes: ['read'], lastSeen: '' },
      { kind: 'connect', id: 'b', name: 'Connect browser', lastSeen: '' },
    ]
    need(reads.shift()).ok(rows)
    await settle(); flushSync()
    assert.equal(elements(target, '.item').length, 2)
    button('Refresh').click()
    flushSync()
    const refreshing = button('Refreshing…')
    assert.equal(refreshing.disabled, true)
    refreshing.click()
    assert.equal(reads.length, 1, 'repeated refresh does not start another read')
    assert.equal(elements(target, '.item').length, 2, 'loaded groups stay visible')
    need(reads.shift()).fail(new Error('refresh failed'))
    await settle(); flushSync()
    assert.equal(elements(target, '.item').length, 2)
    assert.match(target.textContent, /refresh failed/)
    button('Retry').click()
    flushSync()
    need(reads.shift()).ok(rows)
    await settle(); flushSync()
    assert.equal(target.textContent.includes('refresh failed'), false)
    assert.equal(elements(target, '.item').length, 2)

    button('Refresh').click()
    flushSync()
    element(target, '[aria-label="Change scopes for Phone A"]').click()
    flushSync()
    saveButton(row(target, 'Phone A')).click()
    flushSync()
    need(saves()[0]).ok({})
    await settle(); flushSync()
    assert.equal(reads.length, 1)
    need(reads.shift()).ok(rows)
    await settle(); flushSync()
    assert.equal(reads.length, 1, 'a mutation during refresh queues a fresh read')
    need(reads.shift()).ok(rows)
    await settle(); flushSync()
  }, true)
})
