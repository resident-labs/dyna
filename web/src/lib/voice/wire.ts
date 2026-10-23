// The page-to-engine call protocol, in one place: JSON text frames on
// /api/voice/realtime?v=2&room=<name>, with binary PCM16 mono input and
// base64 PCM16 mono output at 24 kHz. Everything else in the call speaks app events; a protocol
// change changes only this file.
import { isRecord } from '../api/guards.ts'
import type { VoicePageMessage } from '../api/voice.ts'

export const RATE = 24000

// TAKEN_OVER is the close code when a newer page took the call.
export const TAKEN_OVER = 4001

// WireEvent is one engine message as an app event. The gateway's JSON is
// untrusted, so a message missing the fields its type needs becomes null.
export type WireEvent =
  | { type: 'resumeCursor'; cursor: string }
  | { type: 'engineState'; value: string }
  | { type: 'voiceLost'; message: string }
  | { type: 'audio'; audio: string; rate: number; responseId: string }
  | { type: 'audioDone'; responseId: string }
  | { type: 'bargeIn'; responseId: string }
  | { type: 'partial'; text: string }
  | { type: 'unspoken'; post: string }
  | { type: 'tool'; id: string; name: string; args: string; state: string }
  | { type: 'serverError'; code: string; message: string }

const ACTIVE = new Set(['listening', 'thinking', 'speaking'])

// text reads a string field, or the empty string for anything else.
const text = (v: unknown): string => (typeof v === 'string' ? v : '')

// fromWire turns one engine message into an app event, or null.
const FROM_WIRE: Record<string, (e: Record<string, unknown>) => WireEvent | null> = {
  resume_cursor: (e) => (typeof e.cursor === 'string' ? { type: 'resumeCursor', cursor: e.cursor } : null),
  state: (e) => (typeof e.state === 'string' && ACTIVE.has(e.state) ? { type: 'engineState', value: e.state } : e.state === 'reconnecting' ? { type: 'voiceLost', message: '' } : null),
  audio: (e) => (typeof e.audio === 'string' && e.audio ? { type: 'audio', audio: e.audio, rate: RATE, responseId: text(e.id) } : null),
  audio_done: (e) => ({ type: 'audioDone', responseId: text(e.id) }),
  clear: (e) => ({ type: 'bargeIn', responseId: text(e.id) }),
  transcript: (e) => ({ type: 'partial', text: e.final ? '' : String(e.text ?? '') }),
  unspoken: (e) => (typeof e.post === 'string' && e.post ? { type: 'unspoken', post: e.post } : null),
  tool: (e) => (typeof e.id === 'string' && e.id ? { type: 'tool', id: e.id, name: text(e.name), args: text(e.text), state: text(e.state) || 'running' } : null),
  error: (e) => ({ type: 'serverError', code: '', message: text(e.message) || 'Voice reported an error.' }),
}

export function fromWire(msg: unknown): WireEvent | null {
  if (!isRecord(msg) || typeof msg.type !== 'string') return null
  const map = FROM_WIRE[msg.type]
  return map ? map(msg) : null
}

// PageMessage is one message the page sends, and every message the engine
// reads from the page.
export type PageMessage = VoicePageMessage

export const audioMsg = (base64: string): PageMessage => ({ type: 'audio', audio: base64 })
// mute is the owner's turn signal: muted:true closes the owner's turn.
export const muteMsg = (muted: boolean): PageMessage => ({ type: 'mute', muted })
export const cancelMsg = (): PageMessage => ({ type: 'cancel' })
export const playbackMsg = (id: string, state: string, playedMs = 0): PageMessage => ({ type: 'playback', id, state, played_ms: Math.max(0, Math.round(playedMs)) })
export const hangupMsg = (): PageMessage => ({ type: 'hangup' })
