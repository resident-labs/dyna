// A floating window's place and size, in viewport pixels.
export type Rect = { x: number; y: number; w: number; h: number };
// The sides one pointer gesture moves: [left, top, right, bottom]. The title bar moves all four.
export type Edges = readonly [0 | 1, 0 | 1, 0 | 1, 0 | 1];
export const MIN = { w: 320, h: 200 };

// Keeps a window inside the viewport and at least its minimum size.
export function clamp(rect: Rect, width: number, height: number): Rect {
  const w = Math.min(Math.max(rect.w, MIN.w), width);
  const h = Math.min(Math.max(rect.h, MIN.h), height);
  return { w, h, x: Math.min(Math.max(rect.x, 0), width - w), y: Math.min(Math.max(rect.y, 0), height - h) };
}

// A window that shows its content's whole frame, keeping its top right corner: the height follows the width, and a
// window that would pass bottom narrows instead. chrome is the height of its title bar and footer.
export function fitted(rect: Rect, ratio: number, chrome: number, bottom: number): Rect {
  const h = Math.max(MIN.h, Math.min(Math.round(rect.w / ratio) + chrome, bottom - rect.y));
  const w = Math.max(MIN.w, Math.min(rect.w, Math.round((h - chrome) * ratio)));
  return { ...rect, w, h, x: rect.x + rect.w - w };
}

// A computer's first place: against the app's right edge, 416 px wide, over the sessions panel. clear says that it
// leaves the cards alone, whose right edge is cards.
export function computerPlace(width: number, top: number, cards: number): { rect: Rect; clear: boolean } {
  const rect = { x: width - 432, y: top, w: 416, h: 300 };
  return { rect, clear: rect.x >= cards + 16 };
}

// The rect after a drag of (dx, dy) on the given edges, from the rect when the drag began.
// A left or top resize that reaches the minimum size stops there instead of pushing the window.
export function dragged(start: Rect, [left, top, right, bottom]: Edges, dx: number, dy: number): Rect {
  const w = left === right ? start.w : Math.max(MIN.w, start.w + (right ? dx : -dx));
  const h = top === bottom ? start.h : Math.max(MIN.h, start.h + (bottom ? dy : -dy));
  return {
    w, h,
    x: left && !right ? start.x + start.w - w : start.x + (left ? dx : 0),
    y: top && !bottom ? start.y + start.h - h : start.y + (top ? dy : 0),
  };
}
