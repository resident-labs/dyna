import type { ForwardClock } from "./forward-clock.ts";

// A delayed swipe belongs to its row. Destroying the row cancels its pending action.
export class SwipeForward {
  private timer: number | undefined;
  private clock: ForwardClock;
  constructor(clock: ForwardClock = {
    set: window.setTimeout.bind(window), clear: window.clearTimeout.bind(window),
  }) { this.clock = clock; }
  start(callback: () => void, delay: number) {
    this.cancel();
    this.timer = this.clock.set(() => { this.timer = undefined; callback(); }, delay);
  }
  cancel() {
    if (this.timer !== undefined) this.clock.clear(this.timer);
    this.timer = undefined;
  }
}
