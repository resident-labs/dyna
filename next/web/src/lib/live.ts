// A computer's live view: connecting until the first frame arrives, then live. It fails when the stream breaks, or
// when no frame arrives within 10 s, because a stream can stay open with no frames (after an engine restart, for
// one). The view keeps the phase it is given; the timer is injected so a test needs no real wait.
export type Phase = "connecting" | "live" | "failed";
export type After = (ms: number, run: () => void) => () => void;

export const firstFrameMs = 10_000;

export class LiveStream {
  private cancel = () => {};
  private readonly set: (phase: Phase) => void;
  private readonly after: After;
  constructor(set: (phase: Phase) => void, after: After = (ms, run) => { const timer = setTimeout(run, ms); return () => clearTimeout(timer); }) {
    this.set = set;
    this.after = after;
  }

  // A new stream: connecting, with the first-frame timer running.
  start(): void {
    this.cancel();
    this.set("connecting");
    this.cancel = this.after(firstFrameMs, () => this.set("failed"));
  }
  frame(): void { this.cancel(); this.set("live"); }
  fail(): void { this.cancel(); this.set("failed"); }
  stop(): void { this.cancel(); }
}
