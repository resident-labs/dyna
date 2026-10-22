// The owner's speech from pressing Call until Voice is ready. The ChatGPT
// app does the same (realtime-buffered-audio-worklet): it keeps up to 30 s of
// microphone audio while it connects, then sends it from 100 ms before the
// first sound, so the owner's first words are never lost.
export const EARLY_MAX_SECONDS = 30
const PRE_ROLL_SECONDS = 0.1
const SILENCE = 0.003 // the ChatGPT app's leading-silence threshold

export interface EarlySpeech {
  push(frame: Float32Array, frameRate: number): void
  take(): { samples: Float32Array; rate: number }
  clear(): void
}

// createEarlySpeech keeps microphone frames, at most EARLY_MAX_SECONDS, newest
// last. take returns them as one buffer without the leading silence and
// empties the buffer.
export function createEarlySpeech(): EarlySpeech {
  let frames: Float32Array[] = []
  let length = 0
  let rate = 0
  const clear = (): void => { frames = []; length = 0 }
  return {
    push(frame, frameRate) {
      if (frameRate !== rate) clear()
      rate = frameRate
      frames.push(frame)
      length += frame.length
      for (;;) {
        const oldest = frames[0]
        if (!oldest) throw new TypeError('earlySpeech: no frame left to trim')
        // NaN fails this comparison, so it stops the trim as before.
        if (!(length - oldest.length >= rate * EARLY_MAX_SECONDS)) break
        length -= oldest.length
        frames.shift()
      }
    },
    take() {
      const all = new Float32Array(length)
      let at = 0
      for (const f of frames) { all.set(f, at); at += f.length }
      clear()
      const first = all.findIndex((v) => Math.abs(v) >= SILENCE)
      const samples = first < 0 ? new Float32Array(0) : all.subarray(Math.max(0, first - Math.round(rate * PRE_ROLL_SECONDS)))
      return { samples, rate }
    },
    clear,
  }
}
