import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { api } from '../../api.ts'
import { createSessionActions } from './sessionActions.svelte.ts'
import type { Member } from '../api/room.ts'

Object.assign(globalThis, { $state: <T>(value: T) => value })

test('closing the session menu restores focus and a closed root cannot be consolidated', () => {
  let focuses = 0, selected = 0
  const actions = createSessionActions({
    api,
    room: () => 'a', request: () => 1, sessionVisit: () => 1, selectedId: () => undefined,
    patchMember() {}, loadSession: async () => {}, loadMembers: async () => {}, loadProfiles() {},
    openSession() {}, openMember() {}, mentionMember() {}, selectForConsolidation() { selected++ },
    moveUnder() {}, expediteItems: () => [], askConfirm() {}, say() {},
  })
  const root: Member = { id: 'root', name: 'root', address: 'root', role: 'root', closed: true, resumable: false, depth: 0, children: 0, status: '', state: 'idle', turns: 0, created: '', lastActivity: '', hue: 0 }
  const anchor = new JSDOM().window.document.createElement('button')
  anchor.focus = () => { focuses++; assert.equal(actions.menu, null) }
  actions.openMenu(root, 0, 0, anchor)
  actions.closeMenu()
  assert.equal(focuses, 1)
  assert.equal(actions.menuItems(root, '').some((item) => item.label === 'Consolidate sessions…'), false)
  assert.equal(selected, 0)
})
