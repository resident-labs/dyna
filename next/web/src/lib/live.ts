// A computer's live view: connecting until the desktop's stream connects, then live. It fails when the stream breaks, or
// when it does not connect within 10 s, because a socket can stay open with nothing on it (after an engine restart, for
// one). The view keeps the phase it is given; the timer is injected so a test needs no real wait.
export type Phase = "connecting" | "live" | "failed";
export type After = (ms: number, run: () => void) => () => void;

export const firstFrameMs = 10_000;

export class LiveStream {
  private cancel = () => {};
  private current = 0;
  private readonly set: (phase: Phase) => void;
  private readonly after: After;
  constructor(set: (phase: Phase) => void, after: After = (ms, run) => { const timer = setTimeout(run, ms); return () => clearTimeout(timer); }) {
    this.set = set;
    this.after = after;
  }

  // A new stream: connecting, with the timer running. Its connection reports through the returned frame and fail, which
  // do nothing once stop or a later start ends the stream, so a closed connection's late events are ignored.
  start(): { frame: () => void; fail: () => void } {
    this.cancel();
    const stream = ++this.current;
    this.set("connecting");
    this.cancel = this.after(firstFrameMs, () => this.set("failed"));
    const end = (phase: Phase) => () => { if (stream === this.current) { this.cancel(); this.set(phase); } };
    return { frame: end("live"), fail: end("failed") };
  }
  stop(): void { this.current++; this.cancel(); }
}
