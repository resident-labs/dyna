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
  // A working session's latest activity label, such as "read spec.md", as the engine sends it.
  readonly activity = new SvelteMap<string, string>();
  // Each working session's current turn, as far as known; a label of another turn is stale.
  private readonly turns = new Map<string, string>();
  // Labels are live only, so each stream connect reloads them. A session whose turn or label changed
  // on the stream after a reload began keeps the newer value.
  private changes = 0;
  private readonly changed = new Map<string, number>();
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
    this.stop = this.connect(undefined, () => this.head, event => this.apply(event), () => this.reload());
  }

  private reload(): void {
    const since = this.changes;
    for (const [room, working] of this.working) {
      if (!working.size) continue;
      // A failed reload keeps the labels as they are, and the next connect tries again.
      void call("session.list", { room }).then(list => {
        for (const { id, turn, activity } of list.sessions ?? []) {
          if (!turn || !working.has(id) || (this.changed.get(id) ?? 0) > since) continue;
          this.turns.set(id, turn);
          if (activity) this.activity.set(id, activity); else this.activity.delete(id);
        }
      }, () => {});
    }
  }

  close(): void { this.stop(); }

  apply(event: Event): void {
    // A live activity label has seq 0, so it comes before the seq check.
    if (event.type === "turn.activity") {
      const { session, turn, label } = event.body;
      if (this.working.get(event.room ?? "")?.has(session) && (this.turns.get(session) ?? turn) === turn) {
        this.activity.set(session, label);
        this.changed.set(session, ++this.changes);
      }
      return;
    }
    if (event.seq <= this.head) return;
    this.head = event.seq;
    const waiting = this.waiting.get(event.room ?? ""), working = this.working.get(event.room ?? "");
    if (!waiting || !working) return;
    switch (event.type) {
      // Only the owner decides owner-class requests; an ancestor agent decides the rest.
      case "op.requested": if (event.body.class === "owner") waiting.add(event.body.id); break;
      case "op.decided": case "op.ran": if (event.body.state !== "pending") waiting.delete(event.body.id); break;
      case "turn.started": this.changed.set(event.body.session, ++this.changes); working.add(event.body.session); this.turns.set(event.body.session, event.body.turn); this.activity.delete(event.body.session); break;
      // A session closed during a turn sends no turn.ended.
      case "turn.ended": case "session.closed": working.delete(event.body.session); this.turns.delete(event.body.session); this.activity.delete(event.body.session); break;
    }
  }
}
