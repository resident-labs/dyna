import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { call, retryDelay } from "./client.ts";
import { stream } from "./stream.ts";
import type { Event, RoomSummary, SessionListResponse, SessionSummary } from "./contract.gen.ts";

// The rooms, and per room the requests waiting for the owner and the sessions working. room.list
// gives them once; the owner stream keeps them current across every room, so nothing polls.
export class Attention {
  readonly rooms = new SvelteMap<string, RoomSummary>();
  readonly waiting = new SvelteMap<string, SvelteSet<string>>();
  readonly working = new SvelteMap<string, SvelteSet<string>>();
  // A working session's latest activity label, such as "read spec.md", as the engine sends it.
  readonly activity = new SvelteMap<string, string>();
  // Per room, the open sessions, once a session.list answer seeds the room; the stream keeps them current.
  readonly sessions = new SvelteMap<string, SvelteSet<string>>();
  // Per room asked but not yet seeded, the session events the stream sent meanwhile; seeding replays the newer ones.
  private readonly pending = new Map<string, Event[]>();
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
    for (const room of list.rooms ?? []) this.add(room);
    this.stop = this.connect(undefined, () => this.head, event => this.apply(event));
  }

  private add(room: RoomSummary): void {
    this.rooms.set(room.id, room);
    this.waiting.set(room.id, new SvelteSet(room.waiting));
    this.working.set(room.id, new SvelteSet(room.working));
  }

  // A room created after the page loaded first shows as an event from a room the page does not know, such as its
  // root's session.started; room.list then names it. One read runs at a time. Events from unknown rooms wait in
  // early while it runs; the answer covers those up to its seq, and the newer ones apply on top. Events from a room
  // the answer does not name yet wait for one more read, as when the read started before the room existed.
  // A failed read keeps them: the stream has moved past them, so only a later read can bring them back. It tries
  // again with the client's capped backoff, or at once when the next unknown room's event arrives.
  private refreshing: Promise<void> | undefined;
  private early: Event[] = [];
  private again = false;
  private tries = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private refresh(): void {
    if (this.refreshing) return;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.refreshing = call("room.list", {}).then(list => {
      this.tries = 0;
      for (const room of list.rooms ?? []) if (!this.rooms.has(room.id)) this.add(room);
      const newer = this.early.filter(event => event.seq > list.seq);
      this.early = newer.filter(event => !this.rooms.has(event.room ?? ""));
      for (const event of newer) if (this.rooms.has(event.room ?? "")) this.applyRoom(event);
      this.again = !this.again && this.early.length > 0;
      if (!this.again) this.early = [];
    }, () => { this.timer = setTimeout(() => this.refresh(), retryDelay(this.tries++)); }).finally(() => {
      this.refreshing = undefined;
      if (this.again && !this.timer) this.refresh();
    });
  }

  close(): void { this.stop(); clearTimeout(this.timer); }

  // A room's sessions for Home, which seed the room's set once. A failed call keeps no buffer.
  listSessions(room: string): Promise<SessionListResponse> {
    if (!this.sessions.has(room)) this.pending.set(room, this.pending.get(room) ?? []);
    return call("session.list", { room }).then(list => { this.seed(list.sessions ?? [], { id: room, seq: list.seq }); return list; },
      (error: unknown) => { this.pending.delete(room); throw error; });
  }

  // Labels are live only, so a session.list answer seeds the turn and label of a session already
  // working; a turn or label the stream sent first is newer and stays. With a room, the answer also seeds that
  // room's set once, and the buffered events after the answer's seq apply on top.
  seed(sessions: Pick<SessionSummary, "id" | "turn" | "activity">[], room?: { id: string; seq: number }): void {
    if (room && !this.sessions.has(room.id)) {
      this.sessions.set(room.id, new SvelteSet(sessions.map(s => s.id)));
      for (const event of this.pending.get(room.id) ?? []) if (event.seq > room.seq) this.track(event);
    }
    if (room) this.pending.delete(room.id);
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
    if (!event.room || this.rooms.has(event.room)) this.applyRoom(event);
    else { this.early.push(event); this.refresh(); }
  }

  private applyRoom(event: Event): void {
    const waiting = this.waiting.get(event.room ?? ""), working = this.working.get(event.room ?? "");
    if (!waiting || !working) return;
    const buffer = this.pending.get(event.room ?? "");
    if (buffer) buffer.push(event); else this.track(event);
    switch (event.type) {
      // Only the owner decides owner-class requests; an ancestor agent decides the rest.
      case "op.requested": if (event.body.class === "owner") waiting.add(event.body.id); break;
      case "op.decided": case "op.ran": if (event.body.state !== "pending") waiting.delete(event.body.id); break;
      case "turn.started": working.add(event.body.session); this.turns.set(event.body.session, event.body.turn); this.activity.delete(event.body.session); break;
      case "turn.ended":
        working.delete(event.body.session); this.turns.delete(event.body.session); this.activity.delete(event.body.session); break;
      // A session closed during a turn sends no turn.ended.
      case "session.closed":
        working.delete(event.body.session); this.turns.delete(event.body.session); this.activity.delete(event.body.session); break;
    }
  }

  // A seeded room's open sessions.
  private track(event: Event): void {
    const sessions = this.sessions.get(event.room ?? "");
    if (event.type === "session.started") sessions?.add(event.body.session);
    if (event.type === "session.closed") sessions?.delete(event.body.session);
  }
}
