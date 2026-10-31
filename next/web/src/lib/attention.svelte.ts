import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import type { Event, RoomSummary, SessionSummary } from "./contract.gen.ts";

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

  // Labels are live only, so a session.list answer seeds the turn and label of a session already
  // working; a turn or label the stream sent first is newer and stays.
  seed(sessions: Pick<SessionSummary, "id" | "turn" | "activity">[]): void {
    for (const { id, turn, activity } of sessions) {
      if (!turn || this.turns.has(id) || ![...this.working.values()].some(set => set.has(id))) continue;
      this.turns.set(id, turn);
      if (activity && !this.activity.has(id)) this.activity.set(id, activity);
    }
  }

  apply(event: Event): void {
    // A live activity label has seq 0, so it comes before the seq check.
    if (event.type === "turn.activity") {
      const { session, turn, label } = event.body;
      if (this.working.get(event.room ?? "")?.has(session) && (this.turns.get(session) ?? turn) === turn) this.activity.set(session, label);
      return;
    }
    // A read position also has seq 0. The feed's moves only forward, so a room store opened later starts from it.
    if (event.type === "conversation.read") {
      const room = this.rooms.get(event.body.room);
      if (room && event.body.conversation.kind === "feed" && event.body.seq > room.lastRead) this.rooms.set(room.id, { ...room, lastRead: event.body.seq });
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
      case "turn.started": working.add(event.body.session); this.turns.set(event.body.session, event.body.turn); this.activity.delete(event.body.session); break;
      // A session closed during a turn sends no turn.ended.
      case "turn.ended": case "session.closed": working.delete(event.body.session); this.turns.delete(event.body.session); this.activity.delete(event.body.session); break;
    }
  }
}
