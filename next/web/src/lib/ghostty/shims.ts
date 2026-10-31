// Small local stand-ins for the T3 Code app helpers that the ported
// terminal files import. They keep the copied files close to upstream.

export const SELECTION_MULTI_CLICK_INTERVAL_MS = 500;

export function isMacPlatform(platform: string): boolean {
  return /mac|iphone|ipad|ipod/i.test(platform);
}

// Dyna passes its own monospace stack, so every family counts as monospace.
export function isMonospaceFamily(_family: string): boolean {
  return true;
}

// observeResize calls back with each size change of the element and returns
// the function that stops watching.
export function observeResize(element: Element, callback: () => void): () => void {
  if (typeof ResizeObserver === "undefined") return () => {};
  const observer = new ResizeObserver(() => callback());
  observer.observe(element);
  return () => observer.disconnect();
}
