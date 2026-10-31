import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import type { Event, RoomSummary } from "./contract.gen.ts";

// The rooms, and per room the requests waiting for the owner and the sessions working. room.list
// gives them once; the owner stream keeps them current across every room, so nothing polls.
export class Attention {
  readonly rooms = new SvelteMap<string, RoomSummary>();
  readonly waiting = new SvelteMap<string, SvelteSet<string>>();
  readonly working = new SvelteMap<string, SvelteSet<string>>();
  // The newest event applied; room.list gives the first value and the stream resumes after it.
  private head = 0;
  private stop = () => {};
  private readonly connect: typeof stream;
  constructor(connect: typeof stream = stream) { this.connect = connect; }

  async open(): Promise<void> {
    const list = await call("room.list", {});
    this.head = list.seq;
    for (const room of list.rooms ?? []) {
      this.rooms.set(room.id, room);
      this.waiting.set(room.id, new SvelteSet(room.waiting));
      this.working.set(room.id, new SvelteSet(room.working));
    }
    this.stop = this.connect(undefined, () => this.head, event => this.apply(event));
  }

  close(): void { this.stop(); }

  apply(event: Event): void {
    if (event.seq <= this.head) return;
    this.head = event.seq;
    const waiting = this.waiting.get(event.room ?? ""), working = this.working.get(event.room ?? "");
    if (!waiting || !working) return;
    switch (event.type) {
      // Only the owner decides owner-class requests; an ancestor agent decides the rest.
      case "op.requested": if (event.body.class === "owner") waiting.add(event.body.id); break;
      case "op.decided": case "op.ran": if (event.body.state !== "pending") waiting.delete(event.body.id); break;
      case "turn.started": working.add(event.body.session); break;
      // A session closed during a turn sends no turn.ended.
      case "turn.ended": case "session.closed": working.delete(event.body.session); break;
    }
  }
}
