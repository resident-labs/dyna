import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
import type { Room } from '../api/room.ts'

const code = await bundle({
  component: '../rooms/RoomRail',
  format: "export const initials = (name) => name.slice(0, 2); export const roomLabel = (room) => room.displayName || room.name;",
})

test('phone rows expose full room names and actions while the desktop keeps its rail', async () => {
  const dom = installDom()
  const ui = await importBundle(code, '../rooms/RoomRail')
  const rooms: Room[] = [{
    id: 'room-long', name: 'infra', displayName: 'Infrastructure planning', lastActivity: '', unread: 104,
    running: 2, members: 3, pinned: false, pinOrder: 0, archived: false,
  }]
  const selected: string[] = []
  const menus: { room: Room; anchor?: HTMLElement }[] = []
  const props = {
    rooms, selected: '', onselect: (id: string) => selected.push(id),
    onmenu: (room: Room, _x: number, _y: number, anchor?: HTMLElement) => menus.push({ room, anchor }),
  }
  const target = document.getElementById('app')
  assert.ok(target)
  try {
    const phone = ui.mount(ui.default, { target, props: { ...props, narrow: true } })
    try {
      ui.flushSync()
      assert.deepEqual([...document.querySelectorAll('.room-name')].map((node) => node.textContent), ['Machines', 'Infrastructure planning'])
      assert.equal(document.querySelector('.badge')?.textContent, '99+')
      const actions = document.querySelector<HTMLButtonElement>('button[aria-label="Actions for Infrastructure planning"]')
      assert.ok(actions)
      actions.click()
      assert.equal(menus[0]?.room.id, 'room-long')
      assert.equal(menus[0]?.anchor, actions)
      document.querySelector<HTMLButtonElement>('button.tile[aria-label="Infrastructure planning"]')?.click()
      assert.deepEqual(selected, ['room-long'])
    } finally {
      await ui.unmount(phone)
    }
    target.replaceChildren()
    const desktop = ui.mount(ui.default, { target, props: { ...props, narrow: false } })
    try {
      ui.flushSync()
      assert.equal(document.querySelector('.room-name'), null)
      assert.equal(document.querySelector('button[aria-label^="Actions for"]'), null)
      assert.equal(document.querySelector('.tile[aria-label="Infrastructure planning"] .ini')?.textContent, 'In')
    } finally {
      await ui.unmount(desktop)
    }
  } finally {
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

