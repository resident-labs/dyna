// The owner's Voice call: one per app window. It asks the engine to start
// the call, opens the call's socket, and moves audio both ways. It is a
// Svelte store of the callState.ts state.
import { capturing, initial, reduce, type CallEvent, type CallState } from './callState.ts'
import { RATE, TAKEN_OVER, cancelMsg, fromWire, hangupMsg, muteMsg, playbackMsg, type PageMessage } from './wire.ts'
import { encodePCMFrame } from './pcm.ts'
import { micError, type AudioHandle, type PlaybackReceipts } from './audio.ts'
import { createEarlySpeech, type EarlySpeech } from './earlySpeech.ts'
import { isRecord } from '../api/guards.ts'

// deps: api (voiceStart, voiceStatus, voiceStop, voiceResume), socket(room) →
// a WebSocket to the room's call, openAudio(onFrame, receipts) → the call's
// audio.

// VoiceReply is what the engine answers a start, resume or status call with.
// It omits a field it has nothing to say about.
export interface VoiceReply {
  available?: boolean
  live?: boolean
  roomId?: string
  room?: string
  startedAt?: number
  resumeCursor?: string
}

// CallApi is the slice of the app's API the call uses.
export interface CallApi {
  voiceStart(roomId: string): Promise<VoiceReply>
  voiceStatus(): Promise<VoiceReply | null>
  voiceStop(): Promise<unknown>
  voiceResume(roomId: string, startedAt: number, cursor: string): Promise<VoiceReply>
}

// CallSocket is the call's socket to the engine, as the call drives it.
export interface CallSocket {
  readyState: number
  send(data: string | ArrayBuffer): void
  close(code?: number): void
  onopen: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent) => void) | null
  onclose: ((event: CloseEvent) => void) | null
}

// CallStore is the call as the views read it: a Svelte store of its state,
// and the controls.
export interface CallStore {
  subscribe(fn: (s: CallState) => void): () => void
  readonly state: CallState
  start(roomId: string, room: string): Promise<void>
  call(roomId: string, room: string): void | Promise<void>
  hangup(): Promise<void>
  switchHere(roomId: string, room: string): Promise<void>
  refresh(): Promise<void>
  cancelSwitch(): void
  dismiss(): void
  toggleMute(): void
  toggleDeafen(): void
  setPushToTalk(on: boolean): void
  talk(on: boolean): void
  hold(on: boolean): void
  interrupt(): void
}

// CallDeps are what the call runs on: the app's API, the room's socket, the
// microphone and player, and the timers (tests drive them).
export interface CallDeps {
  api: CallApi
  socket(room: string): CallSocket
  openAudio(onFrame: (frame: Float32Array, rate: number) => void, receipts: PlaybackReceipts): Promise<AudioHandle>
  // every(ms, fn) repeats fn and returns a cancel.
  every?(ms: number, fn: () => void): () => void
  // after(ms, fn) runs fn once and returns a cancel.
  after?(ms: number, fn: () => void): () => void
}

// Reconnect is a call recovering its socket: its attempts so far, and the
// timers that retry or give up.
interface Reconnect {
  tries: number
  cancelRetry?: () => void
  cancelDeadline?: () => void
}

// micOpen says whether the owner means Voice to hear them: not muted, and
// holding push to talk when it is on. The engine closes the owner's turn
// when it closes (mute), so it is sent whenever it changes.
const micOpen = (s: CallState) => !s.muted && (!s.pushToTalk || s.talking)

// errorText is an error's message, or else the value as text.
const errorText = (err: unknown): string => (isRecord(err) && typeof err.message === 'string' ? err.message : String(err))

// RESUME_WATCH_MS is how often a found call's status is re-read.
export const RESUME_WATCH_MS = 5000
export const RECONNECT_MS = 30000

export function createCall(deps: CallDeps): CallStore {
  const { api, socket, openAudio } = deps
  const every = deps.every ?? ((ms: number, fn: () => void) => { const t = setInterval(fn, ms); return () => clearInterval(t) })
  const after = deps.after ?? ((ms: number, fn: () => void) => { const t = setTimeout(fn, ms); return () => clearTimeout(t) })
  let resumeCursor = '' // issued by the engine; independent of the browser clock
  let reconnect: Reconnect | null = null
  let watch: (() => void) | null = null // re-reads VoiceStatus while a found call waits
  let state = initial
  const subs = new Set<(s: CallState) => void>()
  let ws: CallSocket | null = null
  let audio: AudioHandle | null = null
  let early: EarlySpeech | null = null // the owner's speech from Call until the socket opens
  let attempt = 0 // a newer start or a hangup makes older async steps stale

  const set = (e: CallEvent) => {
    const next = reduce(state, e)
    if (next === state) return
    const prev = state
    state = next
    if (capturing(prev) && !capturing(state)) audio?.unduck()
    if (micOpen(prev) !== micOpen(state)) send(muteMsg(!micOpen(state)))
    if (prev.deafened !== state.deafened) audio?.setDeafened(state.deafened)
    if (state.muted) early?.clear()
    // A call found on page load can end without this page (60 s with no
    // page): keep its status current while the owner decides.
    if (state.phase === 'resumable' && !watch) watch = every(RESUME_WATCH_MS, refresh)
    else if (state.phase !== 'resumable' && watch) { watch(); watch = null }
    for (const fn of subs) fn(state)
  }
  const send = (msg: PageMessage | ArrayBuffer) => { if (ws?.readyState === 1) ws.send(msg instanceof ArrayBuffer ? msg : JSON.stringify(msg)) }

  function release() {
    stopReconnect()
    audio?.close()
    audio = null
    early = null
  }

  function stopReconnect() {
    reconnect?.cancelRetry?.()
    reconnect?.cancelDeadline?.()
    reconnect = null
  }

  function retryConnection(mine: number) {
    if (!reconnect) {
      const pending: Reconnect = { tries: 0 }
      reconnect = pending
      pending.cancelDeadline = after(RECONNECT_MS, () => {
        if (mine !== attempt) return
        const old = ws
        ws = null
        old?.close()
        release()
        set({ type: 'closed', reason: 'Voice could not reconnect. Please call again.' })
      })
    }
    audio?.stopPlayback('clear')
    audio?.unduck()
    early ??= createEarlySpeech()
    set({ type: 'voiceLost' })
    const pending = reconnect
    pending.cancelRetry = after(Math.min(500 * 2 ** pending.tries++, 4000), async () => {
      if (mine !== attempt || reconnect !== pending) return
      try {
        await api.voiceResume(state.roomId, state.startedAt, resumeCursor)
        if (mine !== attempt || reconnect !== pending) return
        connectSocket(mine, state.room)
      } catch {
        if (mine === attempt && reconnect === pending) retryConnection(mine)
      }
    })
  }

  // The player's receipts: what the owner heard of each response, so the
  // engine knows what was said and truncates a reply cut short.
  const receipts: PlaybackReceipts = {
    started: (id) => send(playbackMsg(id, 'started')),
    ended: (id, playedMs) => send(playbackMsg(id, 'ended', playedMs)),
    cancelled: (id, reason, playedMs) => send(playbackMsg(id, 'ended', playedMs)),
  }

  // The engine takes audio from the moment the socket opens and holds it
  // until Voice is ready; before that, the owner's speech waits here.
  function onFrame(frame: Float32Array, rate: number) {
    if (!micOpen(state)) return
    if (ws?.readyState === 1) {
      if (capturing(state)) audio?.hear(frame, rate)
      send(encodePCMFrame(frame, rate, RATE))
    } else early?.push(frame, rate)
  }

  // sendEarly sends the speech that waited for the socket, in messages of
  // one second, and stops keeping it.
  function sendEarly() {
    if (!early) return
    const { samples, rate } = early.take()
    early = null
    for (let i = 0; i < samples.length; i += rate) send(encodePCMFrame(samples.subarray(i, i + rate), rate, RATE))
  }

  function onMessage(raw: string) {
    let msg: unknown
    try { msg = JSON.parse(raw) } catch { return }
    const e = fromWire(msg)
    if (!e) return
    if (e.type === 'resumeCursor') {
      resumeCursor = e.cursor
      return
    }
    if (e.type === 'engineState') stopReconnect()
    switch (e.type) {
      case 'audio':
        audio?.play(e.audio, e.rate, e.responseId)
        return
      case 'audioDone':
        audio?.done(e.responseId)
        return
      case 'bargeIn':
        // The engine heard the owner: stop Voice at once and report what
        // was heard of each response.
        audio?.stopPlayback('clear')
        return
      case 'tool':
        // The row shows where in the conversation Voice called the tool.
        set({ ...e, ts: Date.now() })
        return
      case 'engineState':
        if (state.phase === 'connecting') set({ type: 'voiceReady' })
        set({ type: 'activity', value: e.value })
        return
    }
    set(e)
  }

  async function start(roomId: string, room: string) {
    if (!['idle', 'error', 'resumable'].includes(state.phase)) return
    const mine = ++attempt
    resumeCursor = ''
    set({ type: 'start', roomId, room })
    let opened: AudioHandle
    try {
      opened = await openAudio(onFrame, receipts)
    } catch (err) {
      if (mine === attempt) set({ type: 'startFailed', error: micError(err) })
      return
    }
    if (mine !== attempt) return opened.close()
    audio = opened
    early = createEarlySpeech()
    audio.setDeafened(state.deafened)
    let st: VoiceReply
    try {
      st = await api.voiceStart(roomId)
    } catch (err) {
      if (mine !== attempt) return
      release()
      const now = await api.voiceStatus().catch(() => null)
      if (mine !== attempt) return
      const busyRoom = now?.roomId && now.roomId !== roomId ? now.room : ''
      const error = busyRoom ? `Voice is in a call in ${busyRoom}.` : errorText(err)
      set({ type: 'startFailed', error, busyRoom })
      return
    }
    if (mine !== attempt) return
    resumeCursor = st.resumeCursor || ''
    set({ type: 'started', startedAt: st.startedAt || 0 })
    connectSocket(mine, room)
  }

  function connectSocket(mine: number, room: string) {
    // Every callback belongs to this attempt's socket. After a hang-up and a
    // new call, the old socket's queued events must not touch the new call.
    const sock = socket(room)
    ws = sock
    const current = () => mine === attempt && ws === sock
    sock.onopen = () => {
      if (!current()) return
      sendEarly()
      send(muteMsg(!micOpen(state)))
    }
    sock.onmessage = (m) => { if (current()) onMessage(m.data) }
    sock.onclose = (c) => {
      if (!current()) return
      ws = null
      // An interrupted transport or server restart can recover without
      // closing the microphone or resetting the visible conversation.
      if ([1006, 1012, 1013].includes(c.code)) {
        retryConnection(mine)
        return
      }
      release()
      // 1000: the call ended normally (Hang up elsewhere, or no page for
      // 60 s); 4001: a newer page took it; 1011: an error, with its reason.
      if (c.code === 1000) {
        set({ type: 'hangup' })
        set({ type: 'ended' })
      } else set({ type: 'closed', occupied: c.code === TAKEN_OVER, reason: c.reason || '' })
    }
  }

  async function hangup() {
    if (!['starting', 'connecting', 'live', 'resumable'].includes(state.phase)) return
    attempt++
    set({ type: 'hangup' })
    const old = ws
    send(hangupMsg())
    ws = null
    old?.close(1000)
    release()
    await api.voiceStop().catch(() => {})
    set({ type: 'ended' })
  }

  // call is the header's Call Voice: during a call in another room it asks
  // the owner first (switchTo), else it starts.
  function call(roomId: string, room: string) {
    if (['starting', 'connecting', 'live'].includes(state.phase)) return set({ type: 'askSwitch', roomId, room })
    return start(roomId, room)
  }

  // switchHere ends the call in another room, this window's or one the
  // engine refused a start for, and calls from this one.
  async function switchHere(roomId: string, room: string) {
    if (['starting', 'connecting', 'live'].includes(state.phase)) await hangup()
    else {
      set({ type: 'dismiss' })
      await api.voiceStop()
    }
    await start(roomId, room)
  }

  // refresh reads whether voice is set up. The header asks on mount and
  // after Settings closes, where the owner signs in to ChatGPT.
  // refresh also finds a call the engine still runs after a page load
  // (resumable); the widget asks once when it mounts.
  async function refresh() {
    const st = await api.voiceStatus().catch(() => null)
    if (st) set({ type: 'status', available: !!st.available, live: !!st.live, roomId: st.roomId, room: st.room, startedAt: st.startedAt })
  }


  return {
    subscribe(fn: (s: CallState) => void) {
      subs.add(fn)
      fn(state)
      return () => { subs.delete(fn) }
    },
    get state() { return state },
    start,
    call,
    hangup,
    switchHere,
    refresh,
    cancelSwitch: () => set({ type: 'cancelSwitch' }),
    dismiss: () => set({ type: 'dismiss' }),
    toggleMute: () => set({ type: 'toggleMute' }),
    toggleDeafen: () => set({ type: 'toggleDeafen' }),
    setPushToTalk: (on) => set({ type: 'pushToTalk', on }),
    talk: (on) => set({ type: 'talk', on }),
    hold: (on) => set({ type: 'hold', on }),
    // interrupt stops Voice mid-sentence.
    interrupt() {
      audio?.stopPlayback('cancel')
      send(cancelMsg())
    },
  }
}
