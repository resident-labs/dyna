// The live stream of one agent computer: noVNC over a WebSocket to wayvnc.
//
// The viewer starts view only. The owner takes control on purpose; until
// then, no key or pointer event leaves the page. The stream prefers H.264
// (WebCodecs) when this engine can decode it, and falls back to Tight or
// ZRLE without an error when it cannot. noVNC checks a real H.264 frame at
// load, so an engine that fails the check never asks for H.264. If decoding
// still fails mid-stream, the viewer reconnects once with H.264 turned off.
//
// noVNC has no API for stats, so this file reads private interfaces of the
// pinned version (1.7.0): rfb._display, rfb._decoders, rfb._sendEncodings and RFB.messages.
// Tests and the live computer check cover the encoding switch.

import { Meter } from './rfbStats.js'

let loading = null

// h264Off is true once H.264 failed in this page. It is page-wide because
// the engine that failed once will fail again.
let h264Off = false
export const h264Disabled = () => h264Off

// Each socket keeps its own preference, including when several viewers are open.
const socketModes = new WeakMap()
export const savedStreamMode = (value) => ['sharp', 'smooth'].includes(value) ? value : 'auto'
export const requestedEncodings = (sock, encs) => h264Off || !socketModes.get(sock) ? encs.filter(e => e !== 50) : encs

// Two changed samples within 300 ms mean motion. One sample can be a
// sharper redraw. Ignore small cursor changes. A still picture gets 1.5 s.
export function autoQuality(change, now = performance.now()) {
  let smooth = true
  let lastMotion = now
  let sampledAt = now
  let candidate = -Infinity
  let sharpAt = -Infinity
  let regionAt = -Infinity
  let refreshing = false
  let blocked = false
  let area = 0, left = 1, top = 1, right = 0, bottom = 0
  return {
    keepSmooth() {
      blocked = true
      refreshing = false
      if (!smooth) { smooth = true; change(true) }
    },
    refreshing() { refreshing = true },
    refreshed(time) {
      refreshing = false
      sharpAt = time
      candidate = regionAt = -Infinity
      area = 0; left = 1; top = 1; right = 0; bottom = 0
    },
    scroll(time) {
      refreshing = false
      lastMotion = time
      if (!smooth) { smooth = true; change(true) }
    },
    region(x, y, width, height, screenWidth, screenHeight) {
      if (!screenWidth || !screenHeight) return
      area += width * height / (screenWidth * screenHeight)
      left = Math.min(left, x / screenWidth)
      top = Math.min(top, y / screenHeight)
      right = Math.max(right, (x + width) / screenWidth)
      bottom = Math.max(bottom, (y + height) / screenHeight)
    },
    sample(fraction, time) {
      const fresh = time - sampledAt <= 300
      sampledAt = time
      // Combine tiled updates. Identical terminal lines may only change
      // the tall strip of row numbers along the left edge.
      const width = right - left, height = bottom - top
      const broad = (area >= 0.2 && width >= 0.6 && height >= 0.25)
        || (area >= 0.005 && left <= 0.15 && width >= 0.015 && height >= 0.6)
      // H.264 uses a full-frame rectangle for a caret blink too. Require
      // two broad updates within 300 ms, rather than counting each one.
      const scrolling = broad && time - regionAt <= 300
      if (broad) regionAt = time
      area = 0; left = 1; top = 1; right = 0; bottom = 0
      // The full Sharp redraw can arrive in several pieces.
      if (!smooth && (refreshing || time - sharpAt < 300)) return
      if (fraction >= 0.008 || scrolling) {
        lastMotion = time
        if (!smooth && (scrolling || time - candidate <= 300)) {
          smooth = true
          change(true)
        }
        candidate = time
      } else if (smooth && !blocked && fresh && time - lastMotion >= 1500) {
        smooth = false
        sharpAt = time
        candidate = -Infinity
        change(false)
      }
    },
  }
}

// noVNC 1.7.0 keeps the canvas and connection when sending SetEncodings.
// A full update gives still text its Sharp pixels without waiting for damage.
export function setEncodingMode(rfb, smooth) {
  socketModes.set(rfb._sock, smooth)
  rfb.qualityLevel = smooth ? 6 : 9
  rfb.compressionLevel = smooth ? 2 : 4
  if (rfb._rfbConnectionState !== 'connected') return
  rfb._sendEncodings()
  if (!smooth) rfb.constructor.messages.fbUpdateRequest(rfb._sock, false, 0, 0, rfb._fbWidth, rfb._fbHeight)
}

// Safe WebCodecs pre-check. noVNC runs its own probe while it loads (a
// top-level await), and an engine whose probe throws would make the whole
// import fail, for good. So the page checks first, and hides VideoDecoder
// from noVNC when the check is not clean.
async function webCodecsClean() {
  try {
    if (typeof VideoDecoder === 'undefined' || typeof EncodedVideoChunk === 'undefined') return false
    const r = await VideoDecoder.isConfigSupported({ codec: 'avc1.42401f', codedWidth: 640, codedHeight: 480, optimizeForLatency: true })
    return !!r?.supported
  } catch {
    return false
  }
}

export function loadRFB() {
  loading ??= (async () => {
    const hidden = []
    if (!(await webCodecsClean())) {
      for (const k of ['VideoDecoder']) {
        if (k in window) {
          hidden.push([k, window[k]])
          try { window[k] = undefined } catch {}
        }
      }
    }
    try {
      const mod = await import('@novnc/novnc')
      const RFB = mod.default
      patchEncodings(RFB)
      return RFB
    } finally {
      // Hiding it only needs to last through noVNC's load.
      for (const [k, v] of hidden) { try { window[k] = v } catch {} }
    }
  })()
  return loading
}

// patchEncodings drops H.264 from the list the client sends when the page
// has turned it off.
function patchEncodings(RFB) {
  try {
    const orig = RFB.messages.clientEncodings
    RFB.messages.clientEncodings = (sock, encs) => orig.call(RFB.messages, sock, requestedEncodings(sock, encs))
  } catch {}
}

// instrument counts frames and rectangles. It never throws.
function instrument(rfb, meter, handle, changed, painted) {
  try {
    const d = rfb._display
    const flip = d.flip.bind(d)
    d.flip = (...a) => {
      // noVNC calls flip again when a queued image has decoded.
      const paints = a[0] || !d.pending()
      const result = flip(...a)
      if (paints) { meter.frame(); painted(); handle.onFrame?.() }
      return result
    }
  } catch {}
  try {
    for (const [enc, dec] of Object.entries(rfb._decoders)) {
      const orig = dec.decodeRect.bind(dec)
      dec.decodeRect = (...a) => {
        meter.rect(Number(enc))
        const done = orig(...a)
        if (done) changed(Number(enc), ...a.slice(0, 4))
        return done
      }
    }
  } catch {}
}

const DECODE_ERROR = /failed to decode|videodecoder|h\.?264|webcodecs/i

// openStream connects. target is the element noVNC fills. It returns a
// handle at once and reports progress through on(state, detail):
//   'connecting', 'connected', 'disconnected' (detail.clean, detail.reason),
//   'failed' (detail.reason), 'fallback' (H.264 turned off, reconnecting).
export function openStream({ target, url, on, mode = 'auto' }) {
  let rfb = null
  let ws = null
  let closed = false
  let control = false
  let attempt = 0
  let retryTimer = 0
  let retryDelay = 1000
  mode = savedStreamMode(mode)
  let sampleTimer = 0
  let quality = null
  let sharpRefresh = null
  const sampler = document.createElement('canvas')
  sampler.width = 64
  sampler.height = 40
  const pixels = sampler.getContext('2d', { willReadFrequently: true })
  const meter = new Meter()
  const handle = {
    meter,
    // onFrame runs after each framebuffer flip; the viewer repaints from it.
    onFrame: null,
    get rfb() { return rfb },
    // reconnect opens the stream again after a failure or an end.
    reconnect() {
      retryDelay = 1000
      retried = false
      start()
    },
    setMode(next) {
      mode = savedStreamMode(next)
      const smooth = mode !== 'sharp'
      if (rfb) {
        setEncodingMode(rfb, smooth)
        watchPicture(rfb)
      }
    },
    // setControl turns local input on or off. View only is the default.
    setControl(on) {
      control = on
      if (!rfb) return
      rfb.viewOnly = !on
      rfb.focusOnClick = on
      if (on) rfb.focus()
      else rfb.blur()
    },
    close() {
      closed = true
      attempt++
      clearTimeout(retryTimer)
      clearInterval(sampleTimer)
      for (const type of ['wheel', 'touchmove', 'keydown']) target.removeEventListener(type, scrollInput, true)
      window.removeEventListener('error', onError)
      window.removeEventListener('unhandledrejection', onError)
      try { rfb?.disconnect() } catch {}
      try { ws?.close() } catch {}
    },
  }

  const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])
  function scrollInput(event) {
    if (event.type !== 'keydown' || scrollKeys.has(event.key)) quality?.scroll(performance.now())
  }
  for (const type of ['wheel', 'touchmove', 'keydown']) target.addEventListener(type, scrollInput, { capture: true, passive: true })

  let retried = false
  const onError = (ev) => {
    const msg = String(ev?.message || ev?.reason?.message || ev?.reason || '')
    if (closed || h264Off || !DECODE_ERROR.test(msg)) return
    ev.preventDefault?.()
    h264Off = true
    on('fallback', { reason: 'H.264 did not decode here; using Tight or ZRLE' })
    if (!retried) {
      retried = true
      start()
    }
  }
  window.addEventListener('error', onError)
  window.addEventListener('unhandledrejection', onError)

  function watchPicture(mine) {
    clearInterval(sampleTimer)
    quality = null
    sharpRefresh = null
    if (mode !== 'auto' || !pixels) return
    let before = null
    quality = autoQuality(on => {
      if (!on) {
        quality.refreshing()
        sharpRefresh = { quality, tight: false }
      } else sharpRefresh = null
      setEncodingMode(mine, on)
    })
    sampleTimer = setInterval(() => {
      if (mine !== rfb || mine._rfbConnectionState !== 'connected') return
      // A slow partial frame with low throughput is evidence that Sharp
      // cannot keep up. Keep Auto Smooth until this mode or connection resets.
      if (meter.slowFrame()) quality.keepSmooth()
      const canvas = target.querySelector('canvas')
      if (!canvas?.width) return
      try {
        pixels.drawImage(canvas, 0, 0, 64, 40)
        const after = pixels.getImageData(0, 0, 64, 40).data
        if (before) {
          let changed = 0
          for (let i = 0; i < after.length; i += 4) {
            if (Math.max(Math.abs(after[i] - before[i]), Math.abs(after[i + 1] - before[i + 1]), Math.abs(after[i + 2] - before[i + 2])) > 12) changed++
          }
          quality.sample(changed / (64 * 40), performance.now())
        }
        before = after
      } catch {
        // A browser without readable canvas pixels keeps Smooth.
        clearInterval(sampleTimer)
      }
    }, 100)
  }

  async function start() {
    if (closed) return
    const mineAttempt = ++attempt
    clearTimeout(retryTimer)
    meter.resetFrame()
    clearInterval(sampleTimer)
    quality = null
    control = false
    const previous = rfb
    rfb = null
    try { if (previous && previous._rfbConnectionState !== 'disconnected') previous.disconnect() } catch {}
    try { ws?.close() } catch {}
    on('connecting', {})
    let RFB
    try {
      RFB = await loadRFB()
    } catch (e) {
      if (closed || mineAttempt !== attempt) return
      on('failed', { reason: `The stream viewer could not load: ${e?.message ?? e}` })
      return
    }
    if (closed || mineAttempt !== attempt) return
    try {
      const endpoint = await url()
      if (closed || mineAttempt !== attempt) return
      target.replaceChildren()
      ws = new WebSocket(endpoint, ['binary'])
      let closeCode = null
      let refused = false
      ws.addEventListener('close', e => { closeCode = e.code })
      ws.binaryType = 'arraybuffer'
      ws.addEventListener('message', (e) => {
        meter.received(e.data?.byteLength ?? 0)
        meter.reply()
      })
      const send = ws.send.bind(ws)
      // KeyEvent (4) and PointerEvent (5) are the owner's input. The reply to
      // the next frame update is the delay the owner feels.
      ws.send = (d) => {
        if (d?.[0] === 4 || d?.[0] === 5) meter.sent()
        return send(d)
      }
      const mine = (rfb = new RFB(target, ws, { shared: true }))
      instrument(mine, meter, handle, (encoding, x, y, w, h) => {
        quality?.region(x, y, w, h, mine._fbWidth, mine._fbHeight)
        if (encoding === 7 && sharpRefresh) sharpRefresh.tight = true
      }, () => {
        if (!sharpRefresh?.tight) return
        const refresh = sharpRefresh
        sharpRefresh = null
        mine._display.flush().then(() => {
          if (mine === rfb && quality === refresh.quality) quality.refreshed(performance.now())
        })
      })
      mine.viewOnly = !control
      mine.focusOnClick = control
      mine.scaleViewport = true
      mine.clipViewport = false
      mine.resizeSession = false
      mine.showDotCursor = false
      mine.background = '#000'
      // Tight at high quality for Sharp; noVNC's default is 6.
      setEncodingMode(mine, mode !== 'sharp')
      mine.addEventListener('connect', () => {
        if (mine !== rfb || closed) return
        retryDelay = 1000
        watchPicture(mine)
        on('connected', {})
      })
      mine.addEventListener('disconnect', (e) => {
        if (mine !== rfb || closed) return
        clearInterval(sampleTimer)
        if (refused) return
        // noVNC calls a server-side close clean even for WebSocket 1006.
        // Engine upgrades and lost links must recover without a page reload.
        const retrying = closeCode === 1006
        control = false
        on('disconnected', { clean: !retrying && !!e.detail?.clean, retrying })
        if (retrying) {
          retryTimer = setTimeout(start, retryDelay)
          retryDelay = Math.min(retryDelay * 2, 8000)
        }
      })
      const refuse = reason => {
        if (mine !== rfb || closed) return
        refused = true
        on('failed', { reason })
      }
      mine.addEventListener('securityfailure', e => refuse(e.detail?.reason || 'The stream refused this viewer'))
      mine.addEventListener('credentialsrequired', () => refuse('The stream asked for a password'))
    } catch (e) {
      if (closed || mineAttempt !== attempt) return
      on('failed', { reason: String(e?.message ?? e) })
    }
  }
  start()
  return handle
}
