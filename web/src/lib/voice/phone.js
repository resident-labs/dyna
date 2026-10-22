// The phone layout of the Voice call. Below App's narrow breakpoint the
// floating widget becomes a full-screen call, which minimizes to a bubble.
// The call itself stays in callState.js; this is only the screen's own
// state: minimized, the conversation sheet open, and a call to rejoin hidden.
import { capturing, statusLine } from './callState.js'

// A call starts as the bubble, so it covers little of the screen; tapping
// it opens the full call.
export const phoneInitial = Object.freeze({ minimized: true, sheet: false, resumeHidden: false })

export function phoneReduce(ui, e) {
  switch (e.type) {
    case 'minimize':
      return { ...ui, minimized: true, sheet: false }
    case 'restore':
      return ui.minimized || ui.resumeHidden ? { ...ui, minimized: false, resumeHidden: false } : ui
    case 'hideResume':
      // Only the screen hides; the call the engine runs stays as it is.
      return { ...ui, resumeHidden: true }
    case 'toggleSheet':
      return { ...ui, sheet: !ui.sheet }
    case 'newCall':
      return phoneInitial
  }
  return ui
}

// BUBBLE is the minimized call's size, and TAP_SLOP how far a finger may
// move and still tap it.
export const BUBBLE = 56
export const TAP_SLOP = 6

// SHARE_PEEK is how long the bubble says what was just shared, in ms.
export const SHARE_PEEK = 4000

// snapBubble is where a dropped bubble rests: the nearest side, at the
// height it was dropped, as a share of the screen, kept clear of the top
// and bottom edges.
export function snapBubble({ x, y, width, height }) {
  const margin = BUBBLE / 2 + 8
  const top = Math.min(height - margin, Math.max(margin, y)) / height
  return { side: x < width / 2 ? 'left' : 'right', top: Math.round(top * 1000) / 1000 }
}

// dragged says whether a finger that went down at start and is now at
// (x, y) drags the bubble rather than taps it.
export const dragged = (start, x, y) => Math.hypot(x - start.x, y - start.y) > TAP_SLOP

// HOLD_MS is how long a still press waits before it talks, and
// DOUBLE_TAP_MS how close two taps must come to open the call.
export const HOLD_MS = 250
export const DOUBLE_TAP_MS = 300

// bubbleGestures tells the bubble's gestures apart. A press that moves past
// TAP_SLOP drags it, at any time: a drag ends any talk the press started. A still press talks once held HOLD_MS,
// until it lifts. Two taps within DOUBLE_TAP_MS open the call; one tap does
// nothing, so nothing opens by accident. A click without a pointer (Enter,
// Space, assistive technology) opens the call. The first finger down owns
// the gesture; others are ignored until it lifts, and down reports whether
// it took the pointer. cancel(e) ends the gesture for its owning pointer,
// and cancel() for any reason; it reports whether it ended one. onmove gets the finger while
// it drags, ondrop where it was let go, ontalk true and false as a hold
// starts and ends. clock is setTimeout, clearTimeout and Date.now, each
// called on its own: a browser refuses setTimeout called on another object.
export function bubbleGestures({ onmove, ondrop, onexpand, ontalk }, clock = { set: (fn, ms) => setTimeout(fn, ms), clear: (id) => clearTimeout(id), now: () => Date.now() }) {
  let start = null // where the owning pointer went down, and its ID
  let dragging = false
  let talking = false
  let hold = null
  let tapped = -Infinity // when the last tap lifted
  function end() {
    clock.clear(hold)
    start = null
    dragging = false
    if (talking) {
      talking = false
      ontalk(false)
    }
  }
  return {
    down(e) {
      if (e.button !== 0 || start) return false
      start = { x: e.clientX, y: e.clientY, id: e.pointerId }
      hold = clock.set(() => {
        talking = true
        ontalk(true)
      }, HOLD_MS)
      return true
    },
    move(e) {
      if (start?.id !== e.pointerId) return
      if (dragging || dragged(start, e.clientX, e.clientY)) {
        clock.clear(hold)
        // A press that rested before it moved is still a drag: it ends
        // the talk it started.
        if (talking) {
          talking = false
          ontalk(false)
        }
        dragging = true
        onmove({ x: e.clientX, y: e.clientY })
      }
    },
    up(e) {
      if (start?.id !== e.pointerId) return
      const tap = !dragging && !talking
      if (dragging) ondrop({ x: e.clientX, y: e.clientY })
      end()
      if (!tap) return
      const at = clock.now()
      if (at - tapped > DOUBLE_TAP_MS) return void (tapped = at)
      tapped = -Infinity
      onexpand()
    },
    cancel(e) {
      if (e && start?.id !== e.pointerId) return false
      end()
      return true
    },
    click(e) {
      if (e.detail === 0) onexpand()
    },
  }
}

// bubbleTop is the bubble's CSS top for place: its saved share of the
// screen's height, kept 8 px inside the top and bottom edges whatever the
// screen's height is now.
export const bubbleTop = (place) => `clamp(8px, calc(${place.top * 100}% - ${BUBBLE / 2}px), calc(100% - ${BUBBLE + 8}px))`

// bubblePlace reads a saved bubble place, or the default: the right side,
// a little below the middle.
export function bubblePlace(raw) {
  try {
    const p = JSON.parse(raw)
    if ((p?.side === 'left' || p?.side === 'right') && Number.isFinite(p.top) && p.top > 0 && p.top < 1) return p
  } catch { /* nothing saved */ }
  return { side: 'right', top: 0.6 }
}

// widgetPlace is where the desktop widget rests: its saved place, {right,
// bottom} in px from the window's edges, or the default, clamped into a
// window of width by height. A place saved in a bigger window or on another
// screen would otherwise leave the widget out of sight.
export function widgetPlace(raw, width, height, size = WIDGET) {
  let p = { right: 16, bottom: 96 }
  try {
    const q = JSON.parse(raw)
    if (Number.isFinite(q?.right) && Number.isFinite(q?.bottom)) p = q
  } catch { /* nothing saved */ }
  return clampWidget(p, width, height, size)
}

// WIDGET is the collapsed widget's size, used until it is measured.
export const WIDGET = { width: 300, height: 96 }

// clampWidget keeps the whole widget of size in the window, 8 px inside
// each edge. A widget wider or taller than the window keeps its top left
// corner 8 px in, so its header, drag handle and controls stay reachable.
export const clampWidget = ({ right, bottom }, width, height, size = WIDGET) => ({
  right: within(right, width - size.width - 8),
  bottom: within(bottom, height - size.height - 8),
})
// within keeps an offset from the right or bottom edge between 8 px and max;
// a max below 8 is the widget's size overflowing, and wins.
const within = (v, max) => (max < 8 ? max : Math.max(8, Math.min(max, v)))

// phoneLayout is 'full', 'bubble' or 'none'. A call opens as the bubble; a
// dropped call, a call found on page load, or Call Voice from another room
// while minimized, brings it back full screen; an ended call closes to the app.
// A call found on page load can be hidden to the bubble, which brings Rejoin
// back on a double tap.
export function phoneLayout(s, ui) {
  if (s.phase === 'resumable' && !s.switchTo) return ui.resumeHidden ? 'bubble' : 'full'
  // A dropped call, a call to resume, or a room switch to confirm needs the
  // screen.
  if (s.phase === 'error' || s.phase === 'resumable' || s.switchTo) return 'full'
  if (['starting', 'connecting', 'live', 'ending'].includes(s.phase)) return ui.minimized ? 'bubble' : 'full'
  return 'none'
}

// orb is the big circle: its tone (color and motion) and the state's label.
export function orb(s) {
  if (s.phase === 'error') return { tone: 'error', label: 'Call dropped' }
  if (s.phase !== 'live') return { tone: 'idle', label: statusLine(s) }
  if (s.activity === 'speaking') return { tone: 'speaking', label: 'Speaking' }
  if (s.activity === 'thinking') return { tone: 'thinking', label: 'Thinking' }
  return capturing(s) ? { tone: 'listening', label: 'Listening' } : { tone: 'muted', label: 'Muted' }
}

// bubbleOrb is the bubble's ring: while it is held and the microphone is
// open, Voice listens, even over its own speech.
export const bubbleOrb = (s, held) => (held && capturing(s) ? { tone: 'listening', label: 'Listening' } : orb(s))

// micControl is the one mic button: tap to mute in open mic, hold to talk
// in push to talk.
export function micControl(s) {
  if (s.pushToTalk) return { hold: true, pressed: s.talking, label: s.talking ? 'Talking' : 'Hold to talk' }
  return { hold: false, pressed: s.muted, label: s.muted ? 'Unmute' : 'Mute' }
}

// micModes are the microphone mode menu's items: on is the pushToTalk value
// each one sets, current marks the mode in use.
export const micModes = (s) => [
  { label: 'Open mic', on: false, current: !s.pushToTalk },
  { label: 'Push to talk', on: true, current: s.pushToTalk },
]
