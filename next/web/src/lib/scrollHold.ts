// Corrections wait until input and the resulting scroll events have been quiet for this long.
export const SCROLL_QUIET_MS = 150;

export class ScrollHold {
  private until = 0;
  private delta = 0;
  activity(now: number): void { this.until = now + SCROLL_QUIET_MS; }
  active(now: number): boolean { return now < this.until; }
  add(delta: number): void { this.delta += delta; }
  take(now: number): number {
    if (this.active(now)) return 0;
    const delta = this.delta;
    this.delta = 0;
    return delta;
  }
}
