export const MIN_SCALE = 1
export const MAX_SCALE = 8
// Scale used by double-click when the image already fits.
export const DOUBLE_SCALE = 2

export interface View { scale: number; x: number; y: number }
export interface Point { x: number; y: number }
export interface Size { width: number; height: number }

export const FIT: View = { scale: 1, x: 0, y: 0 }

export const clamp = (v: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, v))

// Keep the image from leaving the box: at scale s the content is content*s wide, and it may move only while it overflows the box.
// `content` is the image size at fit (defaults to the box). Points and offsets are measured from the box center.
export function clampView(view: View, box: Size, content: Size = box): View {
  const scale = clamp(view.scale, MIN_SCALE, MAX_SCALE)
  const mx = Math.max(0, (scale * content.width - box.width) / 2)
  const my = Math.max(0, (scale * content.height - box.height) / 2)
  return { scale, x: clamp(view.x, -mx, mx), y: clamp(view.y, -my, my) }
}

// Change the scale while the content under `at` (relative to the box center) stays under it.
export function zoomAt(view: View, factor: number, at: Point, box: Size, content: Size = box): View {
  const scale = clamp(view.scale * factor, MIN_SCALE, MAX_SCALE)
  const ratio = scale / view.scale
  return clampView({ scale, x: at.x - (at.x - view.x) * ratio, y: at.y - (at.y - view.y) * ratio }, box, content)
}

// Scale at which one image pixel is one screen pixel. When that is barely more than fit, jump to 2x so the change is visible.
export const actualScale = (fitRatio: number): number => {
  const actual = 1 / fitRatio
  return actual >= 1.25 ? Math.min(actual, MAX_SCALE) : DOUBLE_SCALE
}

export function toggleZoom(view: View, at: Point, box: Size, content: Size = box, target: number = DOUBLE_SCALE): View {
  return view.scale > 1.01 ? FIT : zoomAt(view, target, at, box, content)
}

// Wheel deltas map to a smooth exponential factor. A mouse notch (about 100) gives roughly 1.5x.
// A trackpad pinch arrives as a ctrl-wheel with deltas near 1 to 10, so it needs a larger gain.
export const wheelFactor = (deltaY: number, pinch = false): number => Math.exp(-deltaY * (pinch ? 0.015 : 0.004))
