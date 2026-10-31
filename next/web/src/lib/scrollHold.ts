// Corrections wait until input and the resulting scroll events have been quiet for this long.
export const SCROLL_QUIET_MS = 150;

export class ScrollHold {
  private until = 0;
  private delta = 0;
  private touching = false;
  activity(now: number): void { this.until = now + SCROLL_QUIET_MS; }
  touch(down: boolean): void { this.touching = down; }
  active(now: number): boolean { return this.touching || now < this.until; }
  add(delta: number): void { this.delta += delta; }
  take(): number {
    const delta = this.delta;
    this.delta = 0;
    return delta;
  }
}
