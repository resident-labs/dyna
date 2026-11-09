import type { Event } from "./contract.gen.ts";
import { stream, type StreamOpened, type StreamSubscribe } from "./stream.ts";

type Consumer = { room?: string; after: () => number; apply: (event: Event) => void; opened: StreamOpened; seen: boolean };

// Attention and the selected room share one transport. Older engines that do not advertise
// union support retain their two legacy streams rather than silently losing attention events.
export class PageEvents {
  private owner?: Consumer;
  private room?: Consumer;
  private union?: boolean;
  private generation = 0;
  private closed = false;
  private stops: (() => void)[] = [];
  private readonly transport: typeof stream;
  constructor(transport: typeof stream = stream) { this.transport = transport; }

  readonly connect: StreamSubscribe = (room, after, apply, _factory, _schedule, opened = () => {}) => {
    if (this.closed) return () => {};
    const consumer: Consumer = { room, after, apply, opened, seen: false };
    if (room === undefined) this.owner = consumer;
    else this.room = consumer;
    this.rebuild();
    return () => {
      if (room === undefined) {
        if (this.owner !== consumer) return;
        this.owner = undefined;
      } else {
        if (this.room !== consumer) return;
        this.room = undefined;
      }
      this.rebuild();
    };
  };

  private rebuild(): void {
    const generation = ++this.generation;
    for (const stop of this.stops.splice(0)) stop();
    if (this.closed) return;
    const owner = this.owner, room = this.room;
    const start = (primary: Consumer, consumers: Consumer[], attentionAfter?: () => number) => {
      if (generation !== this.generation) return;
      const stop = this.transport(primary.room, primary.after, event => {
        if (generation !== this.generation) return;
        for (const consumer of consumers) {
          if (generation !== this.generation) return;
          if (consumers.length === 1 || consumer.room === undefined || consumer.room === event.room) consumer.apply(event);
        }
      }, undefined, undefined, (reconnect = false, hello) => {
        if (generation !== this.generation) return;
        const supported = hello?.roomUnion === true;
        const changed = this.union !== supported;
        this.union = supported;
        // Wait for a capability hello before sending a query an old engine would ignore.
        if (changed && this.owner && this.room) { this.rebuild(); return; }
        for (const consumer of consumers) {
          if (generation !== this.generation) return;
          const again = consumer.seen || reconnect;
          consumer.seen = true;
          consumer.opened(again, hello);
        }
      }, undefined, attentionAfter);
      // A synchronous test transport/consumer can replace the registration during hello.
      if (generation === this.generation) this.stops.push(stop);
      else stop();
    };
    if (owner && room && this.union === true) start(room, [owner, room], owner.after);
    else if (owner && room && this.union === false) { start(owner, [owner]); start(room, [room]); }
    else { const primary = owner ?? room; if (primary) start(primary, [primary]); }
  }

  close(): void {
    this.closed = true;
    this.owner = this.room = undefined;
    this.rebuild();
  }
}
