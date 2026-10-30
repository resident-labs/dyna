import { errorText } from '../errorText.ts'
import { roomLabel } from '../ui/format.ts'
import type { ActionRoom } from '../ui/actions.ts'
import type { ToastInput } from '../ui/toast.ts'

export interface PromptSpec {
  title: string
  lines?: string[]
  label?: string
  value?: string
  multiline?: boolean
  placeholder?: string
  match?: string
  action?: string
  danger?: boolean
  submit: (value: string) => void | Promise<unknown>
}

interface RoomActionsHost {
  roomId(): string
  selectRoom(id: string): Promise<unknown>
  loadRooms(): Promise<unknown>
  say(input: ToastInput): void
  renameRoom(id: string, name: string): Promise<unknown>
  deleteRoom(id: string): Promise<unknown>
}

export function createRoomActions(host: RoomActionsHost) {
  let prompt = $state<PromptSpec | null>(null)
  return {
    get prompt() { return prompt },
    open(value: PromptSpec) { prompt = value },
    dismiss() { prompt = null },
    async action(fn: () => Promise<unknown>, done: string) {
      try {
        await fn()
        host.say({ tone: 'success', subject: done })
        await host.loadRooms()
      } catch (e) {
        host.say({ tone: 'error', subject: 'Could not finish that room action', text: errorText(e) })
      }
    },
    rename(r: ActionRoom) {
      prompt = {
        title: 'Rename room',
        label: 'Name',
        value: roomLabel(r),
        lines: [`The new name shows in the app. Agents still address the room as #${r.name}.`],
        action: 'Rename',
        submit: async (name) => {
          await host.renameRoom(r.id, name)
          prompt = null
          host.say({ tone: 'success', subject: 'Room renamed', text: `It now shows as ${name}.` })
          host.loadRooms()
        },
      }
    },
    delete(r: ActionRoom) {
      prompt = {
        title: `Delete ${roomLabel(r)}?`,
        lines: ['This removes the room, its feed and its pins. Records of closed sessions stay.', 'A room with live work cannot be deleted. Stop its sessions and runs first.', `Type ${r.name} to confirm.`],
        label: 'Room name',
        placeholder: r.name,
        match: r.name,
        action: 'Delete room',
        danger: true,
        submit: async () => {
          await host.deleteRoom(r.id)
          prompt = null
          host.say({ tone: 'success', subject: 'Room deleted', text: `${roomLabel(r)} and its feed are gone.` })
          if (host.roomId() === r.id) host.selectRoom('')
          host.loadRooms()
        },
      }
    },
  }
}
