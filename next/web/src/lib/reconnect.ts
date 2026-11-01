import type { Schedule } from "./stream.ts";

// reconnectNotice shows the reconnecting notice only after the event stream has been down for graceMs.
// A short drop shows nothing. A failed retry during the wait does not restart the timer.
export function reconnectNotice(
  show: (on: boolean) => void,
  schedule: Schedule = (callback, delay) => {
    const id = setTimeout(callback, delay);
    return () => clearTimeout(id);
  },
  graceMs = 2000,
): (up: boolean) => void {
  let cancel: (() => void) | undefined;
  let shown = false;
  return up => {
    if (up) {
      cancel?.();
      cancel = undefined;
      if (shown) { shown = false; show(false); }
      return;
    }
    if (cancel || shown) return;
    cancel = schedule(() => { cancel = undefined; shown = true; show(true); }, graceMs);
  };
}
