// PCM16 helpers for the call: the gateway takes and gives mono 16-bit
// little-endian PCM as base64.

// downsample averages each output sample's span of input, which also keeps
// most aliasing out. Rates at or below the target pass through.
export function downsample(input: Float32Array, from: number, to: number): Float32Array {
  if (!to || from <= to) return input
  const ratio = from / to
  const out = new Float32Array(Math.floor(input.length / ratio))
  for (let i = 0; i < out.length; i++) {
    const start = Math.floor(i * ratio)
    const end = Math.min(input.length, Math.floor((i + 1) * ratio))
    let sum = 0
    for (const v of input.subarray(start, end)) sum += v
    out[i] = end > start ? sum / (end - start) : 0
  }
  return out
}

export function floatToPCM16(input: Float32Array): Int16Array {
  const out = new Int16Array(input.length)
  for (const [i, sample] of input.entries()) {
    const v = Math.max(-1, Math.min(1, sample))
    out[i] = v < 0 ? v * 0x8000 : v * 0x7fff
  }
  return out
}

export function pcm16ToFloat(pcm: Int16Array): Float32Array {
  const out = new Float32Array(pcm.length)
  for (const [i, sample] of pcm.entries()) out[i] = sample / 0x8000
  return out
}

export function bytesToBase64(bytes: Uint8Array): string {
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(s)
}

export function base64ToBytes(b64: string): Uint8Array {
  const s = atob(b64)
  const out = new Uint8Array(s.length)
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i)
  return out
}

// encodeFrame turns one microphone frame into the base64 PCM16 the gateway
// takes at rate to.
export function encodeFrame(frame: Float32Array, from: number, to: number): string {
  const pcm = floatToPCM16(downsample(frame, from, to))
  return bytesToBase64(new Uint8Array(pcm.buffer, pcm.byteOffset, pcm.byteLength))
}

// decodeAudio turns a gateway audio delta into samples to play.
export function decodeAudio(b64: string): Float32Array {
  const bytes = base64ToBytes(b64)
  return pcm16ToFloat(new Int16Array(bytes.buffer, bytes.byteOffset, bytes.byteLength >> 1))
}

// silence is ms of zero PCM16 at rate, as base64.
export const silence = (rate: number, ms: number): string => bytesToBase64(new Uint8Array(Math.round(rate * ms / 1000) * 2))
