// Ducking: Voice gets quieter the moment the owner starts talking, before
// the gateway's turn detection stops it, and comes back if they stop. The
// numbers are the ChatGPT app's (voice-dsp AssistantPlaybackDucker): 10 dB
// down, 20 ms attack, 800 ms release, 300 ms hold, at least 750 ms ducked,
// then a 1 s ramp back. ChatGPT detects speech with a Silero model; this uses
// the microphone frame's energy instead.
export const DUCKED = 10 ** (-10 / 20)
const ATTACK = 0.02
const RELEASE = 0.8
const HOLD = 0.3
const MIN_DUCKED = 0.75
const RESTORE = 1
// SPEECH is the frame RMS that counts as the owner talking, about -34 dBFS
// after the browser's echo cancelling and gain control. A guess: tune it
// from live calls.
const SPEECH = 0.02

const WINDOW = 0.01 // ChatGPT's VAD frame: a single click fills one window

// The parts of an AudioParam the ducker drives.
export interface DuckGain {
  cancelScheduledValues(startTime: number): void
  setTargetAtTime(target: number, startTime: number, timeConstant: number): void
  setValueAtTime(value: number, startTime: number): void
  linearRampToValueAtTime(value: number, endTime: number): void
}

export interface Ducker {
  hear(frame: Float32Array, rate: number, now: number): void
  reset(now: number): void
}

const rms = (samples: Float32Array): number => {
  let sum = 0
  for (const v of samples) sum += v * v
  return Math.sqrt(sum / (samples.length || 1))
}

// createDucker drives gain, an AudioParam. hear takes each microphone frame,
// its sample rate and the context time at its end; reset ends ducking.
export function createDucker(gain: DuckGain): Ducker {
  let loudFor = 0 // seconds of speech in a row
  let quietSince: number | null = null
  let duckedAt: number | null = null
  function step(loud: boolean, length: number, now: number): void {
    if (loud) {
      loudFor += length
      quietSince = null
      if (duckedAt === null && loudFor >= ATTACK - 1e-9) {
        duckedAt = now
        gain.cancelScheduledValues(now)
        gain.setTargetAtTime(DUCKED, now, 0.01)
      }
      return
    }
    loudFor = 0
    quietSince ??= now - length
    if (duckedAt !== null && now - quietSince >= RELEASE - 1e-9) {
      const from = Math.max(duckedAt + MIN_DUCKED, now + HOLD)
      gain.setValueAtTime(DUCKED, from)
      gain.linearRampToValueAtTime(1, from + RESTORE)
      duckedAt = null
    }
  }
  return {
    hear(frame, rate, now) {
      const size = Math.max(1, Math.round(rate * WINDOW))
      const start = now - frame.length / rate
      for (let i = 0; i < frame.length; i += size) {
        const window = frame.subarray(i, i + size)
        step(rms(window) >= SPEECH, window.length / rate, start + (i + window.length) / rate)
      }
    },
    reset(now) {
      loudFor = 0
      quietSince = null
      duckedAt = null
      gain.cancelScheduledValues(now)
      gain.setValueAtTime(1, now)
    },
  }
}
