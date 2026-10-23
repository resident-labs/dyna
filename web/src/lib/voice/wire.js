// The page-to-engine call protocol, in one place: JSON text frames on
// /api/voice/realtime?v=2&room=<name>, with binary PCM16 mono input and
// base64 PCM16 mono output at 24 kHz. Everything else in the call speaks app events; a protocol
// change changes only this file.

export const RATE = 24000

// TAKEN_OVER is the close code when a newer page took the call.
export const TAKEN_OVER = 4001

const ACTIVE = new Set(['listening', 'thinking', 'speaking'])

// fromWire turns one engine message into an app event, or null.
const FROM_WIRE = {
  resume_cursor: (e) => (typeof e.cursor === 'string' ? { type: 'resumeCursor', cursor: e.cursor } : null),
  state: (e) => (ACTIVE.has(e.state) ? { type: 'engineState', value: e.state } : e.state === 'reconnecting' ? { type: 'voiceLost', message: '' } : null),
  audio: (e) => (e.audio ? { type: 'audio', audio: e.audio, rate: RATE, responseId: e.id || '' } : null),
  audio_done: (e) => ({ type: 'audioDone', responseId: e.id || '' }),
  clear: (e) => ({ type: 'bargeIn', responseId: e.id || '' }),
  transcript: (e) => ({ type: 'partial', text: e.final ? '' : String(e.text ?? '') }),
  unspoken: (e) => (e.post ? { type: 'unspoken', post: e.post } : null),
  tool: (e) => (e.id ? { type: 'tool', id: e.id, name: e.name || '', args: e.text || '', state: e.state || 'running' } : null),
  error: (e) => ({ type: 'serverError', code: '', message: e.message || 'Voice reported an error.' }),
}

export function fromWire(msg) {
  const map = FROM_WIRE[msg?.type]
  return map ? map(msg) : null
}

export const audioMsg = (base64) => ({ type: 'audio', audio: base64 })
// mute is the owner's turn signal: muted:true closes the owner's turn.
export const muteMsg = (muted) => ({ type: 'mute', muted })
export const cancelMsg = () => ({ type: 'cancel' })
export const playbackMsg = (id, state, playedMs = 0) => ({ type: 'playback', id, state, played_ms: Math.max(0, Math.round(playedMs)) })
export const hangupMsg = () => ({ type: 'hangup' })
