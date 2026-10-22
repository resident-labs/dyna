// The live event stream at /api/events, with resume (item 14).
//
// Every event carries an ID, "epoch:rev", where the epoch is the engine
// start. A reconnect sends the last ID back, and the server replays what the
// client missed. After an engine restart, or when the client fell behind
// what the server keeps, the server sends one reset instead, and the client
// emits {kind: "resync"} once so the app reloads what it shows.
//
// Some proxies hold a stream's body until it ends; Cloudflare quick tunnels
// do. The server starts every stream with a hello event. If none arrives
// within 6 seconds of the stream opening, the client closes it and
// long-polls /api/events/poll instead, for the life of the page.
//
// The server pings every 15 seconds. A stream that is silent for 30 seconds
// is dead even if the socket looks open, so the client opens a new one.
//
// A phone freezes timers while the page is hidden or the network is gone.
// When the browser goes online or the page shows again, the client reopens a
// lost or silent stream at once, instead of waiting out its backoff. A stream
// that is still connecting, or that was heard from in the last 30 seconds,
// stays. In long-poll mode, a wake ends the backoff sleep, or cuts short a
// poll in flight, which a dead network can hold open, and polls again.

import { isBoolean, isNumber, isRecord, optional } from './guards.ts'

// StreamMessage is the part of a MessageEvent the client reads.
export interface StreamMessage {
  data: string
  lastEventId: string
}

// StreamSource is the part of EventSource the client uses.
export interface StreamSource {
  onopen: (() => void) | null
  onmessage: ((e: StreamMessage) => void) | null
  onerror: (() => void) | null
  readonly readyState: number
  addEventListener(type: 'hello' | 'reset' | 'ping', fn: (e: StreamMessage) => void): void
  close(): void
}

// StreamIO is how the client reaches the server. Tests replace it.
export interface StreamIO {
  open: (url: string) => StreamSource
  poll: (url: string, signal: AbortSignal) => Promise<Response>
  // probe makes a small call, so that a revoked device sees its 401.
  probe: () => void
  // wake runs fn when the browser goes online or the page shows again.
  // It returns a function that stops listening.
  wake: (fn: () => void) => () => void
}

// The value of EventSource.CLOSED.
const closed = 2
const helloWait = 6000
// Twice the server's 15 second ping.
const silenceWait = 30000

interface PollReply {
  epoch: number
  seq: number
  events?: unknown[]
  reset?: boolean
}

function isPollReply(v: unknown): v is PollReply {
  return isRecord(v) && isNumber(v.epoch) && isNumber(v.seq) && optional(v.events, Array.isArray) && optional(v.reset, isBoolean)
}

// openStream delivers each event to fn as parsed JSON, and {kind: "resync"}
// when the app must reload. state runs with false when the stream is lost
// and true when it is back. It returns a function that stops the stream.
export function openStream(fn: (ev: unknown) => void, state: (up: boolean) => void, io: StreamIO): () => void {
  let stopped = false
  let es: StreamSource | null = null
  // failed marks the current source as unusable after an error, so a wake
  // replaces it even when traffic arrived within the silence window.
  let failed = false
  let lastId = ''
  let retry: ReturnType<typeof setTimeout> | undefined
  let helloTimer: ReturnType<typeof setTimeout> | undefined
  let silenceTimer: ReturnType<typeof setTimeout> | undefined
  let wait = 3000
  let lost = false
  let polling = false
  // heardAt is when the current stream opened or last sent anything.
  let heardAt = 0
  // nap ends the poll loop's backoff sleep early; abortPoll ends a poll in
  // flight. woken tells the loop that a wake did it.
  let nap = () => {}
  let abortPoll = () => {}
  let woken = false
  const setLost = (v: boolean) => {
    if (lost !== v) state(!v)
    lost = v
  }
  const resync = () => fn({ kind: 'resync' })
  const at = (e: StreamMessage) => {
    if (e.lastEventId) lastId = e.lastEventId
  }

  // reopen replaces the stream, resuming from the last event.
  const reopen = () => {
    es?.close()
    clearTimeout(helloTimer)
    clearTimeout(silenceTimer)
    setLost(true)
    clearTimeout(retry)
    retry = setTimeout(stream, wait)
    wait = Math.min(30000, wait * 2)
  }
  // heard re-arms the silence timer on any traffic.
  const heard = () => {
    heardAt = Date.now()
    clearTimeout(silenceTimer)
    silenceTimer = setTimeout(reopen, silenceWait)
  }

  const stream = () => {
    if (stopped) return
    const source = io.open(lastId ? `/api/events?last=${encodeURIComponent(lastId)}` : '/api/events')
    es = source
    heardAt = Date.now()
    failed = false
    source.onopen = () => {
      failed = false
      clearTimeout(helloTimer)
      helloTimer = setTimeout(() => {
        clearTimeout(silenceTimer)
        source.close()
        polling = true
        void poll()
      }, helloWait)
    }
    source.addEventListener('hello', (e) => {
      clearTimeout(helloTimer)
      at(e)
      heard()
      setLost(false)
      wait = 3000
    })
    source.addEventListener('reset', (e) => {
      at(e)
      heard()
      resync()
    })
    source.addEventListener('ping', heard)
    source.onmessage = (e) => {
      at(e)
      heard()
      let ev: unknown
      try {
        ev = JSON.parse(e.data)
      } catch {
        return
      }
      fn(ev)
    }
    source.onerror = () => {
      setLost(true)
      failed = true
      // EventSource retries a dropped stream by itself, sending the last ID,
      // but gives up after an error status (a 401, or a proxy error while
      // the server restarts). Then open a new one, waiting longer each time,
      // up to 30 seconds.
      if (source.readyState === closed && !stopped) {
        io.probe()
        reopen()
      }
    }
  }

  const poll = async () => {
    let after = lastId
    // Without a position, the poll cannot know what was missed.
    if (after === '') resync()
    while (!stopped) {
      const request = new AbortController()
      abortPoll = () => request.abort()
      try {
        const r = await io.poll(`/api/events/poll${after === '' ? '' : `?after=${encodeURIComponent(after)}`}`, request.signal)
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        const body: unknown = await r.json()
        if (!isPollReply(body)) throw new Error('bad poll reply')
        if (body.reset) resync()
        else for (const ev of body.events ?? []) fn(ev)
        setLost(false)
        wait = 3000
        after = `${body.epoch}:${body.seq}`
      } catch {
        if (stopped) return
        // A wake cut the poll short: poll again now, with no loss reported.
        if (woken) {
          woken = false
          continue
        }
        setLost(true)
        await new Promise<void>((ok) => {
          const t = setTimeout(ok, wait)
          nap = () => {
            clearTimeout(t)
            ok()
          }
        })
        // A wake also resets the backoff.
        if (woken) woken = false
        else wait = Math.min(30000, wait * 2)
      }
    }
  }

  // woke reconnects now when the stream is lost or has gone silent.
  const woke = () => {
    if (stopped) return
    if (polling) {
      wait = 3000
      woken = true
      abortPoll()
      nap()
      return
    }
    if (es && es.readyState !== closed && !failed && Date.now() - heardAt < silenceWait) return
    wait = 3000
    es?.close()
    clearTimeout(retry)
    clearTimeout(helloTimer)
    clearTimeout(silenceTimer)
    setLost(true)
    stream()
  }
  const unwake = io.wake(woke)

  stream()
  return () => {
    stopped = true
    unwake()
    abortPoll()
    nap()
    clearTimeout(retry)
    clearTimeout(helloTimer)
    clearTimeout(silenceTimer)
    es?.close()
  }
}
