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
