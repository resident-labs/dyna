// The Voice call's state, as a plain reducer over app events. wire.ts turns
// gateway messages into these events; the controller adds its own (start,
// started, closed, hangup and the controls). The view only reads.
//
// phase: idle → starting (the engine starts the gateway) → connecting (the
// socket waits for voice.ready) → live → ending → idle. error holds a failed
// start or a dropped call until the owner calls again or closes it.
// resumable is a call the engine still runs after a page load: Rejoin
// starts it here (starting), End hangs it up (ending → idle).

// Phase is where the call is in its life.
export type Phase = 'idle' | 'starting' | 'connecting' | 'live' | 'ending' | 'error' | 'resumable'

// VoiceTool is one tool Voice called during the call, by its place in time.
export interface VoiceTool {
  id: string
  name: string
  args: string
  state: string
  ts: number
}

// SwitchTo is the room the owner asked to call from instead.
export interface SwitchTo {
  roomId: string
  room: string
}

export interface CallState {
  phase: Phase
  activity: string // listening, thinking, speaking or idle, while live
  roomId: string
  room: string
  startedAt: number // unix ms, from the engine: the call's posts come after it
  connected: boolean // Voice has answered at least once in this call
  error: string
  busyRoom: string // the room whose call blocked a start
  muted: boolean
  deafened: boolean
  pushToTalk: boolean
  talking: boolean // push to talk is held
  holdMuted: boolean // the phone bubble is held, and the mic was muted before
  partial: string // the owner's words as they are heard
  available: boolean // VoiceStatus.available: voice is set up
  unspoken: string[] // post IDs the engine never got Voice to say
  tools: VoiceTool[] // Voice's tool calls in this call
  switchTo: SwitchTo | null // the owner asked to call from another room
}

// CallEvent is one change to the call: an app event from wire.ts, or one the
// controller raises itself.
export type CallEvent =
  | { type: 'start'; roomId: string; room: string }
  | { type: 'status'; available: boolean; live?: boolean; roomId?: string; room?: string; startedAt?: number }
  | { type: 'askSwitch'; roomId: string; room: string }
  | { type: 'cancelSwitch' }
  | { type: 'started'; startedAt?: number }
  | { type: 'startFailed'; error: string; busyRoom?: string }
  | { type: 'voiceReady' }
  | { type: 'voiceLost'; message?: string }
  | { type: 'activity'; value: string }
  | { type: 'partial'; text: string }
  | { type: 'serverError'; code?: string; message: string }
  | { type: 'closed'; occupied?: boolean; reason?: string }
  | { type: 'hangup' }
  | { type: 'ended' }
  | { type: 'unspoken'; post: string }
  | { type: 'tool'; id: string; name: string; args: string; state: string; ts: number }
  | { type: 'dismiss' }
  | { type: 'toggleMute' }
  | { type: 'toggleDeafen' }
  | { type: 'hold'; on: boolean }
  | { type: 'pushToTalk'; on: boolean }
  | { type: 'talk'; on: boolean }

export const initial: CallState = Object.freeze({
  phase: 'idle',
  activity: 'idle',
  roomId: '',
  room: '',
  startedAt: 0,
  connected: false,
  error: '',
  busyRoom: '',
  muted: false,
  deafened: false,
  pushToTalk: false,
  talking: false,
  holdMuted: false,
  partial: '',
  available: false,
  unspoken: [],
  tools: [],
  switchTo: null,
})

const inCall = (s: CallState): boolean => ['starting', 'connecting', 'live'].includes(s.phase)

export function reduce(s: CallState, e: CallEvent): CallState {
  switch (e.type) {
    case 'start':
      if (inCall(s) || s.phase === 'ending') return s
      return { ...initial, muted: s.muted, deafened: s.deafened, pushToTalk: s.pushToTalk, available: s.available, phase: 'starting', roomId: e.roomId, room: e.room }
    case 'status': {
      // A page load finds a call the engine still runs (a reload): offer to
      // resume it. Rejoin is the owner's choice; a load never starts audio.
      const next = s.available === e.available ? s : { ...s, available: e.available }
      if (s.phase === 'idle' && e.live) return { ...next, phase: 'resumable', roomId: e.roomId ?? '', room: e.room ?? '', startedAt: e.startedAt || 0 }
      if (s.phase === 'resumable' && !e.live) return { ...next, phase: 'idle' }
      return next
    }
    case 'askSwitch':
      return inCall(s) && e.roomId !== s.roomId ? { ...s, switchTo: { roomId: e.roomId, room: e.room } } : s
    case 'cancelSwitch':
      return s.switchTo ? { ...s, switchTo: null } : s
    case 'started':
      return s.phase === 'starting' ? { ...s, phase: 'connecting', startedAt: e.startedAt || 0 } : s
    case 'startFailed':
      return s.phase === 'starting' ? { ...s, phase: 'error', error: e.error, busyRoom: e.busyRoom || '' } : s
    case 'voiceReady':
      if (s.phase !== 'connecting') return s
      return { ...s, phase: 'live', activity: 'listening', error: '', connected: true }
    case 'voiceLost':
      return s.phase === 'live' ? { ...s, phase: 'connecting', activity: 'idle', partial: '' } : s
    case 'activity':
      return s.phase === 'live' ? { ...s, activity: e.value } : s
    case 'partial':
      return inCall(s) ? { ...s, partial: e.text } : s
    case 'serverError':
      if (!inCall(s)) return s
      if (e.code === 'client_occupied') return { ...s, phase: 'error', error: occupied }
      return { ...s, error: e.message }
    case 'closed':
      if (s.phase === 'ending' || s.phase === 'idle') return { ...s, phase: 'idle', activity: 'idle', talking: false, partial: '' }
      if (s.phase === 'error') return s
      return { ...s, phase: 'error', activity: 'idle', talking: false, partial: '', error: e.occupied ? occupied : e.reason || DROPPED }
    case 'hangup':
      // End on a call found on page load is a hang-up too.
      return inCall(s) || s.phase === 'resumable' ? { ...s, phase: 'ending', talking: false, partial: '', switchTo: null } : s
    case 'ended':
      return s.phase === 'ending' ? { ...s, phase: 'idle', activity: 'idle' } : s
    case 'unspoken':
      return s.unspoken.includes(e.post) ? s : { ...s, unspoken: [...s.unspoken, e.post] }
    case 'tool': {
      if (!inCall(s)) return s
      const i = s.tools.findIndex((t) => t.id === e.id)
      if (i < 0) return { ...s, tools: [...s.tools, { id: e.id, name: e.name, args: e.args, state: e.state, ts: e.ts }] }
      const old = s.tools[i]
      return old ? { ...s, tools: s.tools.with(i, { ...old, state: e.state }) } : s
    }
    case 'dismiss':
      return s.phase === 'error' ? { ...s, phase: 'idle', error: '', busyRoom: '' } : s
    case 'toggleMute':
      return s.muted ? { ...s, muted: false, deafened: false } : { ...s, muted: true, talking: false }
    case 'toggleDeafen':
      // Deafen also mutes, and undeafen unmutes, as in Discord.
      return s.deafened ? { ...s, deafened: false, muted: false } : { ...s, deafened: true, muted: true, talking: false }
    case 'hold':
      // Holding the phone bubble is a radio button: it talks in push to
      // talk, and opens a muted mic only for the hold. A deafened call stays
      // silent; an open mic stays as it is.
      if (s.deafened) return s
      if (e.on) return { ...s, muted: false, holdMuted: s.muted || s.holdMuted, talking: s.pushToTalk }
      return { ...s, muted: s.muted || s.holdMuted, holdMuted: false, talking: false }
    case 'pushToTalk':
      // The mic button is the talk control in either mode, so a mode change
      // unmutes; a deafened call stays muted. Choosing the current mode
      // changes nothing, so an explicit mute stays.
      if (e.on === s.pushToTalk) return s
      return { ...s, pushToTalk: e.on, talking: false, muted: s.deafened }
    case 'talk':
      return s.pushToTalk ? { ...s, talking: e.on } : s
  }
  return s
}

// DROPPED is the error of a call that ended without a reason.
export const DROPPED = 'The call dropped.'

const occupied = 'Voice is open in another window or app. Close it there, then call again.'

// capturing says whether the microphone's audio goes to Voice now.
export const capturing = (s: CallState): boolean => s.phase === 'live' && !s.muted && (!s.pushToTalk || s.talking)

// statusLine is the one line under Voice's name. During a call it is
// Listening, Speaking, Thinking or Muted.
export function statusLine(s: CallState): string {
  switch (s.phase) {
    case 'resumable': return 'Call in progress'
    case 'starting': return 'Starting Voice…'
    case 'connecting': return s.connected ? 'Reconnecting…' : 'Connecting…'
    case 'ending': return 'Ending the call…'
    case 'error': return s.error
    case 'live':
      // Only the state: the controls show how to change it.
      if (s.activity === 'speaking') return 'Speaking'
      if (s.activity === 'thinking') return 'Thinking'
      return capturing(s) ? 'Listening' : 'Muted'
  }
  return ''
}

// Offer is the room header's call button: no button, or what it shows.
export type Offer = '' | 'live' | 'calling' | 'call'

// offerCall decides the room header's call button: 'live' or 'calling' for
// this room's call, 'call' when voice is set up (VoiceStatus.available) or a
// call runs elsewhere, else nothing, so the header stays as it was.
export function offerCall(s: CallState, roomId: string): Offer {
  if (s.roomId === roomId && s.phase === 'live') return 'live'
  if (s.roomId === roomId && ['starting', 'connecting'].includes(s.phase)) return 'calling'
  return s.available || inCall(s) ? 'call' : ''
}

// TalkTarget is what a key, blur or visibility event targets: the element
// with focus, or the page.
export interface TalkTarget {
  hidden?: boolean
  isContentEditable?: boolean
  closest?: (selector: string) => unknown
}

// TalkKey is the part of a key, blur or visibility event push to talk reads.
export interface TalkKey {
  type: string
  code?: string
  repeat?: boolean
  target?: TalkTarget | null
}

// spaceTalk reads a key, blur or visibility event for push to talk: true
// to talk, false to stop, or null to leave it alone. Space starts talking
// only outside a text field (the composer, search, any input, textarea or
// editable text), so typing a space never opens the microphone. Any Space
// release stops talking, wherever focus went, and so does leaving the
// window or hiding the page, which lose the release.
export function spaceTalk(e: TalkKey, s: CallState): boolean | null {
  if (!s.pushToTalk || s.phase !== 'live') return null
  if (e.type === 'blur' || (e.type === 'visibilitychange' && e.target?.hidden)) return s.talking ? false : null
  if (e.code !== 'Space') return null
  if (e.type === 'keyup') return s.talking ? false : null
  if (e.type !== 'keydown' || e.repeat) return null
  const t = e.target
  if (t?.isContentEditable || t?.closest?.('input, textarea, select, [contenteditable]:not([contenteditable="false"])')) return null
  return true
}
