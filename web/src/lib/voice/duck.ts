// Optional microphone-driven playback attenuation. Server speech detection
// controls interruption independently. Require sustained energy above the
// recent noise floor so background noise and brief bursts do not lower Voice.
export const DUCKED = 10 ** (-10 / 20)
const ATTACK = 0.15
const RELEASE = 0.8
const HOLD = 0.3
const MIN_DUCKED = 0.75
const RESTORE = 1
const WINDOW = 0.01 // Measure energy in 10 ms windows.
const HISTORY = 200 // Two seconds of recent RMS measurements.

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
  const noise: number[] = []
  let noiseAt = 0
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
        const level = rms(window)
        // Do not let sustained speech raise its own noise floor after ducking.
        if (duckedAt === null) {
          noise[noiseAt] = level
          noiseAt = (noiseAt + 1) % HISTORY
        }
        const sorted = [...noise].sort((a, b) => a - b)
        const floor = sorted[Math.floor((sorted.length - 1) * 0.1)] ?? level
        const threshold = floor + Math.max(0.005, floor * 2)
        step(level > threshold, window.length / rate, start + (i + window.length) / rate)
      }
    },
    reset(now) {
      noise.length = 0
      noiseAt = 0
      loudFor = 0
      quietSince = null
      duckedAt = null
      gain.cancelScheduledValues(now)
      gain.setValueAtTime(1, now)
    },
  }
}
