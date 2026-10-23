// The app's one Voice call. It lives outside every view, so the call goes on
// while the owner moves between rooms and views.
import { api } from '../../api.ts'
import { openAudio } from './audio.ts'
import { createCall } from './call.ts'
import { createCallRequests } from './callRequests.ts'
import { createScreenLock } from './wakeLock.ts'
import { inCall } from './callState.ts'
import type { SettingItem } from '../api/settings.ts'

async function configuredAudio(...[onFrame, receipts]: Parameters<typeof openAudio>) {
  const items: SettingItem[] = (await api.settings().catch(() => [])) ?? []
  const ducking = items.some((item) => item.key === 'voice.ducking' && item.value === true)
  return openAudio(onFrame, receipts, ducking)
}

export const voiceCall = createCall({ api, socket: (room: string) => new WebSocket(`${api.voiceURL()}?v=2&room=${encodeURIComponent(room)}`), openAudio: configuredAudio })
export const callRequests = createCallRequests(api)

// The screen stays on while a call runs: a phone locks its screen when it sees
// no touch, and a call has none. The lock follows the call's phase.
const screenLock = createScreenLock()
voiceCall.subscribe((s) => { void screenLock.keep(inCall(s)) })

// clock formats a call's length as m:ss, or h:mm:ss.
export function clock(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000))
  const mm = String(Math.floor(s / 60) % 60).padStart(2, '0')
  const ss = String(s % 60).padStart(2, '0')
  return s >= 3600 ? `${Math.floor(s / 3600)}:${mm}:${ss}` : `${mm}:${ss}`
}
