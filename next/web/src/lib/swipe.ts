// Pure touch gesture rules. The caller owns touch identifiers and browser events.
export type Swipe = { x: number; y: number; at: number; dragging: boolean; ticked: boolean };
export type Move = { swipe: Swipe | null; distance: number; tick: boolean };
export const ignoredSwipeTarget = "input, textarea, select, iframe, pre, table, [contenteditable]";

export function beginSwipe(x: number, y: number, at: number, coarse: boolean, ignored: boolean, selected: boolean): Swipe | null {
  return coarse && !ignored && !selected && x >= 24 ? { x, y, at, dragging: false, ticked: false } : null;
}

export function moveSwipe(swipe: Swipe, x: number, y: number, at: number, cancelable: boolean, selected: boolean): Move {
  const dx = x - swipe.x, dy = y - swipe.y;
  if (!cancelable || (!swipe.dragging && (at - swipe.at > 500 || selected || (Math.abs(dy) > 12 && Math.abs(dy) >= Math.abs(dx)) || dx < -12))) {
    return { swipe: null, distance: 0, tick: false };
  }
  if (!swipe.dragging && (dx < 12 || Math.abs(dx) < Math.abs(dy) * 1.4)) return { swipe, distance: 0, tick: false };
  const distance = Math.max(0, dx), tick = distance >= 64 && !swipe.ticked;
  return { swipe: { ...swipe, dragging: true, ticked: swipe.ticked || tick }, distance: Math.min(96, distance <= 64 ? distance : 64 + (distance - 64) / 3), tick };
}

export function releaseSwipe(swipe: Swipe, x: number): boolean {
  return swipe.dragging && x - swipe.x >= 64;
}
