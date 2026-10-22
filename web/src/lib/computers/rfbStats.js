// Client-side numbers for the viewer's stats line: frames per second,
// bitrate, input latency and the codec in use. The meter is pure, so node
// tests can drive it with a fake clock. computerStream.js feeds it from noVNC.

// ENCODING names the RFB encodings wayvnc and noVNC use.
export const ENCODING = { 0: 'Raw', 1: 'CopyRect', 2: 'RRE', 5: 'Hextile', 6: 'Zlib', 7: 'Tight', 8: 'TightPNG', 16: 'ZRLE', 21: 'JPEG', 50: 'H.264' }

export class Meter {
  constructor(now = () => performance.now(), window = 2000) {
    this.now = now
    this.window = window
    this.frames = []
    this.bytes = []
    this.rects = new Map()
    this.sentAt = null
    this.latency = null
    this.latencyAt = 0
    this.resetFrame()
  }

  resetFrame() { this.pendingAt = null; this.pendingBytes = 0 }
  frame() { this.frames.push(this.now()); this.resetFrame() }
  received(n) {
    const now = this.now()
    this.bytes.push([now, n])
    if (n > 0) {
      this.pendingBytes += n
      // Tiny control messages can arrive while idle. Time substantial data.
      if (this.pendingBytes >= 65536) this.pendingAt ??= now
    }
  }
  // A partial frame waits at least 1 s, with under 512 KiB/s received.
  slowFrame() {
    const now = this.now()
    if (this.pendingAt === null || now - this.pendingAt < 1000 || this.pendingBytes < 65536) return false
    const from = now - this.window
    const bytes = this.bytes.reduce((sum, [time, n]) => sum + (time >= from ? n : 0), 0)
    return bytes / (this.window / 1000) < 524288
  }
  rect(enc) {
    if (enc === 1 || enc === undefined) return // CopyRect moves pixels; it does not name the stream's codec
    this.rects.set(enc, [...(this.rects.get(enc) ?? []), this.now()])
  }
  // sent marks input leaving the page; the next reply measures the delay.
  sent() { this.sentAt = this.now() }
  reply() {
    if (this.sentAt === null) return
    this.latency = this.now() - this.sentAt
    this.latencyAt = this.now()
    this.sentAt = null
  }

  _trim(list, t, key = (x) => x) {
    while (list.length && key(list[0]) < t) list.shift()
  }

  snapshot() {
    const t = this.now()
    const from = t - this.window
    this._trim(this.frames, from)
    this._trim(this.bytes, from, (x) => x[0])
    for (const [enc, list] of this.rects) {
      this._trim(list, from)
      if (!list.length) this.rects.delete(enc)
    }
    const secs = this.window / 1000
    let top = null
    for (const [enc, list] of this.rects) if (!top || list.length > top[1]) top = [enc, list.length]
    return {
      fps: Math.round(this.frames.length / secs),
      mbit: this.bytes.reduce((n, x) => n + x[1], 0) * 8 / 1e6 / secs,
      codec: top ? (ENCODING[top[0]] ?? `encoding ${top[0]}`) : '',
      // A latency older than 10 s says nothing about now.
      latency: this.latency !== null && t - this.latencyAt < 10000 ? Math.round(this.latency) : null,
    }
  }
}

// statsLine is the text of the viewer's stats row. The codec is what this
// page decoded in the last two seconds, never what the daemon hopes it sends.
// The encoder is the daemon's (VA-API, software).
export function statsLine(snap, status) {
  const codec = snap.codec || '—'
  const parts = [`codec ${codec}`]
  if (status?.stream?.encoder) parts.push(`encoder ${status.stream.encoder}`)
  parts.push(`${snap.fps} fps`, `${snap.mbit < 10 ? snap.mbit.toFixed(1) : Math.round(snap.mbit)} Mbit/s`, `latency ${snap.latency === null ? '—' : snap.latency + ' ms'}`)
  return parts
}
