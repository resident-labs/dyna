// The call's audio in the browser: one AudioContext for the microphone and
// for Voice's speech, so the browser's echo canceller hears both.
import { decodeAudio } from './pcm.ts'
import { createDucker, type Ducker } from './duck.ts'
import { isRecord } from '../api/guards.ts'

// The worklet must load from the app origin: the page's CSP allows no data:
// or blob: scripts. vite.config.ts emits the worklet as its own script,
// mic-worklet.js, and the dev server serves the same source at that path.
const workletURL = '/mic-worklet.js'

// PlaybackReceipts are what the owner heard of each response: started when
// its first chunk plays, ended with the milliseconds played, cancelled with
// the milliseconds heard when it is dropped.
export interface PlaybackReceipts {
  started(id: string): void
  ended(id: string, playedMs: number): void
  cancelled(id: string, reason: string, playedMs: number): void
}

// PlaybackBuffer is the part of an AudioBuffer the player fills.
export interface PlaybackBuffer {
  getChannelData(channel: number): Float32Array
}

// PlaybackSource is the part of an AudioBufferSourceNode the player drives.
export interface PlaybackSource {
  buffer: PlaybackBuffer | null
  onended: ((event: Event) => void) | null
  connect(destination: unknown): void
  start(when?: number): void
  stop(): void
}

// PlaybackContext is the part of an AudioContext the player drives.
export interface PlaybackContext {
  readonly currentTime: number
  createBuffer(channels: number, length: number, sampleRate: number): PlaybackBuffer
  createBufferSource(): PlaybackSource
}

// Player plays Voice's audio and reports what the owner heard.
export interface Player {
  play(b64: string, rate: number, id: string): void
  done(id: string): void
  stop(reason: string): void
}

// Playback is one response's audio: the sources that carry it, whether the
// engine finished it and whether the owner heard any of it.
interface Playback {
  sources: Set<PlaybackSource>
  done: boolean
  started: boolean
  timer: ReturnType<typeof setTimeout> | null
  startAt: number
  dur: number
}

// createPlayer queues Voice's audio chunks back to back into out, and
// reports playback receipts per response: started when its first chunk
// actually starts to play, ended with the milliseconds played once its
// audio is done and has drained, cancelled with the milliseconds heard
// when stop drops it. the engine counts started as heard, so a response
// queued behind another reports started only at its scheduled start. stop
// drops everything queued at once, for barge-in, with any pending started.
// out is the node the call's audio goes through; the player only connects
// to it.
export function createPlayer(ctx: PlaybackContext, out: unknown, receipts: PlaybackReceipts): Player {
  let next = 0
  const responses = new Map<string, Playback>()
  const finish = (id: string) => {
    const r = responses.get(id)
    if (!r || !r.done || !r.started || r.sources.size) return
    responses.delete(id)
    receipts.ended(id, r.dur * 1000)
  }
  const start = (id: string, r: Playback) => {
    r.timer = null
    r.started = true
    r.startAt = ctx.currentTime
    receipts.started(id)
    finish(id)
  }
  return {
    play(b64, rate, id) {
      const samples = decodeAudio(b64)
      if (!samples.length) return
      const buffer = ctx.createBuffer(1, samples.length, rate)
      buffer.getChannelData(0).set(samples)
      const src = ctx.createBufferSource()
      src.buffer = buffer
      src.connect(out)
      const at = Math.max(ctx.currentTime, next)
      src.start(at)
      next = at + samples.length / rate
      let r = responses.get(id)
      if (!r) {
        const fresh: Playback = { sources: new Set(), done: false, started: false, timer: null, startAt: 0, dur: 0 }
        responses.set(id, fresh)
        const wait = (at - ctx.currentTime) * 1000
        if (wait > 1) fresh.timer = setTimeout(() => start(id, fresh), wait)
        else start(id, fresh)
        r = fresh
      }
      r.dur += samples.length / rate
      r.sources.add(src)
      src.onended = () => {
        r.sources.delete(src)
        finish(id)
      }
    },
    done(id) {
      const r = responses.get(id)
      if (!r) return
      r.done = true
      finish(id)
    },
    stop(reason) {
      for (const [id, r] of responses) {
        if (r.timer !== null) clearTimeout(r.timer)
        for (const src of r.sources) {
          src.onended = null
          try { src.stop() } catch { /* already ended */ }
        }
        // played is what the owner heard: the engine truncates the reply
        // there, and a response that never started was not heard at all.
        const played = r.started ? Math.min(r.dur, ctx.currentTime - r.startAt) * 1000 : 0
        if (reason) receipts.cancelled(id, reason, played)
      }
      responses.clear()
      next = 0
    },
  }
}

// AudioHandle is the call's audio: Voice's playback both ways, the
// microphone's frames, and the gain stages deafen and ducking drive.
export interface AudioHandle {
  play(b64: string, rate: number, id: string): void
  done(id: string): void
  stopPlayback(reason: string): void
  setDeafened(on: boolean): void
  hear(frame: Float32Array, rate: number): void
  unduck(): void
  close(): void
}

// openAudio asks for the microphone and starts capture. onFrame gets each
// frame and the context's sample rate; receipts get the player's playback
// receipts. It rejects with the browser's error when the microphone is
// refused or missing.
export async function openAudio(onFrame: (frame: Float32Array, rate: number) => void, receipts: PlaybackReceipts, ducking = false): Promise<AudioHandle> {
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
  })
  const ctx = new AudioContext()
  try {
    await ctx.audioWorklet.addModule(workletURL)
    const source = ctx.createMediaStreamSource(stream)
    const mic = new AudioWorkletNode(ctx, 'dyna-mic')
    mic.port.onmessage = (e: MessageEvent<Float32Array>) => onFrame(e.data, ctx.sampleRate)
    source.connect(mic)
    await ctx.resume()
  } catch (err) {
    stream.getTracks().forEach((t) => t.stop())
    ctx.close()
    throw err
  }
  // Deafen turns out down to silence. Voice's audio still plays through,
  // so its receipts keep their timing. Optional ducking adds a separate
  // gain stage; by default mic energy never changes playback volume.
  const out = ctx.createGain()
  const duck: GainNode | null = ducking ? ctx.createGain() : null
  out.connect(duck ?? ctx.destination)
  duck?.connect(ctx.destination)
  const ducker: Ducker | null = duck ? createDucker(duck.gain) : null
  const player = createPlayer(ctx, out, receipts)
  return {
    play: player.play,
    done: player.done,
    stopPlayback: player.stop,
    setDeafened(on) { out.gain.value = on ? 0 : 1 },
    hear: (frame, rate) => ducker?.hear(frame, rate, ctx.currentTime),
    unduck: () => ducker?.reset(ctx.currentTime),
    close() {
      player.stop('')
      stream.getTracks().forEach((t) => t.stop())
      ctx.close()
    },
  }
}

// micError says in plain words why the microphone did not open.
export function micError(err: unknown): string {
  const name = isRecord(err) && typeof err.name === 'string' ? err.name : ''
  switch (name) {
    case 'NotAllowedError': return 'Dyna may not use the microphone. Allow it, then call again.'
    case 'NotFoundError': return 'No microphone was found.'
    case 'NotReadableError': return 'Another app is using the microphone.'
  }
  const message = isRecord(err) && typeof err.message === 'string' ? err.message : ''
  return `The microphone did not start: ${message || String(err)}`
}
