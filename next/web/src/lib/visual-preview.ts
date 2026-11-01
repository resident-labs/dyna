// Limit live preview documents across the feed. Waiting cards acquire a slot when one leaves view.
const waiting = new Set<() => void>();
let active = 0;
export function acquirePreview(start: () => void): () => void {
  let held = false;
  const take = () => { held = true; active++; start(); };
  if (active < 2) take(); else waiting.add(take);
  return () => {
    waiting.delete(take);
    if (held) {
      held = false; active--;
      const next = waiting.values().next().value;
      if (next) { waiting.delete(next); next(); }
    }
  };
}

// A capture is 960 by 360 whatever the page's length, so a short page leaves a blank band at the bottom.
const previewHeight = 360;
export type PreviewFit = { height: number; clipped: boolean };

// The rows at the top of RGBA pixels that hold content. A page's background reaches the bottom row as one color, so the
// count stops at the last row with another color. A bottom row of mixed colors is content running past the capture.
export function contentRows(pixels: Uint8ClampedArray, width: number, height: number, tolerance = 6): number {
  const background = pixels.subarray((height - 1) * width * 4, (height - 1) * width * 4 + 4);
  for (let row = height - 1; row >= 0; row--) {
    for (let x = 0; x < width; x++) {
      const at = (row * width + x) * 4;
      if ([0, 1, 2, 3].some(channel => Math.abs(pixels[at + channel] - background[channel]) > tolerance)) return row + 1;
    }
  }
  return 0;
}

// The preview's height, in capture pixels, for content of this many rows out of height: the content with a margin below
// it, at least a third of the capture. Only content that reaches the capture's bottom was clipped.
export function previewFit(rows: number, height = previewHeight): PreviewFit {
  if (rows >= height) return { height: previewHeight, clipped: true };
  return { height: Math.min(previewHeight, Math.max(previewHeight / 3, Math.ceil(rows * previewHeight / height) + 32)), clipped: false };
}

// A live preview cannot be measured, as its sandbox hides the page, so it has the height the card declares. A declared
// height at or past the capture's may run on, and 360 is also the default, so that keeps the fade.
export const declaredFit = (height: number): PreviewFit => height < previewHeight ? previewFit(height) : { height: previewHeight, clipped: true };
