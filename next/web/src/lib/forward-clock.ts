// Each conversation gets its own Undo expiry. A clock is injected for fast tests.
export type ForwardClock = {
  set: (callback: () => void, milliseconds: number) => number;
  clear: (timer: number) => void;
};

export class ForwardExpiry {
  private timers = new Map<string, number>();
  private expire: (key: string) => void;
  private clock: ForwardClock;
  constructor(expire: (key: string) => void, clock: ForwardClock = {
    set: window.setTimeout.bind(window), clear: window.clearTimeout.bind(window),
  }) { this.expire = expire; this.clock = clock; }
  clear(key: string) {
    const timer = this.timers.get(key);
    if (timer !== undefined) this.clock.clear(timer);
    this.timers.delete(key);
  }
  start(key: string) {
    this.clear(key);
    this.timers.set(key, this.clock.set(() => { this.timers.delete(key); this.expire(key); }, 5000));
  }
}
