// The call's audio in the browser: one AudioContext for the microphone and
// for Voice's speech, so the browser's echo canceller hears both.
import { decodeAudio } from './pcm.ts'
import { createDucker } from './duck.ts'

// The worklet must load from the app origin: the page's CSP allows no data:
// or blob: scripts, so Vite must not inline it.
const workletURL = new URL('./micWorklet.js?no-inline', import.meta.url).href

// createPlayer queues Voice's audio chunks back to back into out, and
// reports playback receipts per response: started when its first chunk
// actually starts to play, ended with the milliseconds played once its
// audio is done and has drained, cancelled with the milliseconds heard
// when stop drops it. the engine counts started as heard, so a response
// queued behind another reports started only at its scheduled start. stop
// drops everything queued at once, for barge-in, with any pending started.
export function createPlayer(ctx, out, receipts) {
  let next = 0
  const responses = new Map() // response ID -> { sources, done, started, timer }
  const finish = (id) => {
    const r = responses.get(id)
    if (!r || !r.done || !r.started || r.sources.size) return
    responses.delete(id)
    receipts.ended(id, r.dur * 1000)
  }
  const start = (id, r) => {
    r.timer = 0
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
        r = { sources: new Set(), done: false, started: false, timer: 0, startAt: 0, dur: 0 }
        responses.set(id, r)
        const wait = (at - ctx.currentTime) * 1000
        if (wait > 1) r.timer = setTimeout(() => start(id, r), wait)
        else start(id, r)
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
        clearTimeout(r.timer)
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

// openAudio asks for the microphone and starts capture. onFrame gets each
// frame and the context's sample rate; receipts get the player's playback
// receipts. It rejects with the browser's error when the microphone is
// refused or missing.
export async function openAudio(onFrame, receipts, ducking = false) {
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
  })
  const ctx = new AudioContext()
  try {
    await ctx.audioWorklet.addModule(workletURL)
    const source = ctx.createMediaStreamSource(stream)
    const mic = new AudioWorkletNode(ctx, 'dyna-mic')
    mic.port.onmessage = (e) => onFrame(e.data, ctx.sampleRate)
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
  const duck = ducking ? ctx.createGain() : null
  out.connect(duck || ctx.destination)
  duck?.connect(ctx.destination)
  const ducker = duck ? createDucker(duck.gain) : null
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
export function micError(err) {
  switch (err?.name) {
    case 'NotAllowedError': return 'Dyna may not use the microphone. Allow it, then call again.'
    case 'NotFoundError': return 'No microphone was found.'
    case 'NotReadableError': return 'Another app is using the microphone.'
  }
  return `The microphone did not start: ${err?.message || err}`
}
