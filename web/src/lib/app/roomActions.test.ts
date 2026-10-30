import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { stripTypes } from '../../../test/svelte-source.ts'
import { roomLabel } from '../ui/format.ts'
import { errorText } from '../errorText.ts'

test('a delayed delete does not change a newly selected room', async () => {
  const source = readFileSync(new URL('./roomActions.svelte.ts', import.meta.url), 'utf8')
  const code = stripTypes(source).replace(/^import .*$/gm, '').replace('export function', 'function')
  const context = vm.createContext({ roomLabel, errorText, $state: (value: unknown) => value })
  vm.runInContext(code, context)
  const create = vm.runInContext('createRoomActions', context)
  let room = 'a'
  let reloads = 0
  let finish: () => void = () => { throw new Error('delete not started') }
  const pending = new Promise<void>((resolve) => { finish = resolve })
  const actions = create({
    roomId: () => room,
    selectRoom: async (id: string) => { room = id },
    loadRooms: async () => { reloads++ },
    say: () => {},
    renameRoom: async () => {},
    deleteRoom: () => pending,
  })
  actions.delete({ id: 'a', name: 'a' })
  const deleting = actions.prompt.submit('')
  room = 'b'
  finish()
  await deleting
  assert.equal(room, 'b')
  assert.equal(reloads, 1)
})
