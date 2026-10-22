// The app's one Voice call. It lives outside every view, so the call goes on
// while the owner moves between rooms and views.
import { api } from '../../api.js'
import { openAudio } from './audio.js'
import { createCall } from './call.js'
import { createCallRequests } from './callRequests.ts'
import type { SettingItem } from '../api/settings.ts'

async function configuredAudio(...[onFrame, receipts]: Parameters<typeof openAudio>) {
  const items: SettingItem[] = await api.settings().catch(() => [])
  const ducking = items?.some((item) => item.key === 'voice.ducking' && item.value === true) ?? false
  return openAudio(onFrame, receipts, ducking)
}

export const voiceCall = createCall({ api, socket: (room: string) => new WebSocket(`${api.voiceURL()}?v=2&room=${encodeURIComponent(room)}`), openAudio: configuredAudio })
export const callRequests = createCallRequests(api)

// clock formats a call's length as m:ss, or h:mm:ss.
export function clock(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000))
  const mm = String(Math.floor(s / 60) % 60).padStart(2, '0')
  const ss = String(s % 60).padStart(2, '0')
  return s >= 3600 ? `${Math.floor(s / 3600)}:${mm}:${ss}` : `${mm}:${ss}`
}
