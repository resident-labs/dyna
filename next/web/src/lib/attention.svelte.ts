import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { APIError, blockedBy, call, retryDelay } from "./client.ts";
import { stream } from "./stream.ts";
import { boxFailures, turnFailures, type BoxFailure } from "./status.ts";
import { pendingReason } from "./event.ts";
import { scopes as everyScope } from "./devices.ts";
import type { Event, FailureCause, RoomSummary, Scope, SessionListResponse, SessionSummary } from "./contract.gen.ts";

// The rooms, and per room the requests waiting for the owner and the sessions with an open turn. room.list
// gives them once; the owner stream keeps them current across every room, so nothing polls.
export class Attention {
  readonly rooms = new SvelteMap<string, RoomSummary>();
  readonly waiting = new SvelteMap<string, SvelteSet<string>>();
  readonly working = new SvelteMap<string, SvelteSet<string>>();
  // A working session's latest activity label, such as "read spec.md", as the engine sends it. The newest label is
  // last, so a room's busiest agent is its last working entry; room.list seeds each room's newest one.
  readonly activity = new SvelteMap<string, string>();
  // Per room, the open sessions, once a session.list answer seeds the room; the stream keeps them current.
  readonly sessions = new SvelteMap<string, SvelteSet<string>>();
  // Per room, the sessions whose box failed or was lost. A session.list answer seeds them, since room.list has no box
  // states. Such a session is not working, even with its turn open (status.ts).
  readonly broken = new SvelteMap<string, SvelteMap<string, BoxFailure>>();
  // Per room, the sessions whose last turn failed, with that turn.ended's seq and cause. room.list gives only a count, so a room
  // with one reads its sessions when it loads; the stream keeps every room current.
  readonly failed = new SvelteMap<string, SvelteMap<string, { seq: number; cause?: FailureCause }>>();
  // Per room, the sessions whose box waits for a host that takes the room, which only the owner can end. room.list
  // gives only a count, so a room with one reads its sessions as for a failure; the stream keeps every room current.
  readonly hostless = new SvelteMap<string, SvelteSet<string>>();
  // Each session's DM read position, for the same rule as RoomStore.unseen.
  private readonly read = new SvelteMap<string, number>();
  // Per room asked but not yet seeded, the session events the stream sent meanwhile; seeding replays the newer ones.
  private readonly pending = new Map<string, Event[]>();
  // Each working session's current turn, as far as known; a label of another turn is stale.
  private readonly turns = new Map<string, string>();
  // What this device may do, read once when the page connects. An engine without device.scopes answers not_found; the page
  // then offers every control, and the engine still refuses what the device cannot do.
  private readonly scopes = new SvelteSet<Scope>(everyScope.map(item => item.scope));
  can = (scope: Scope) => this.scopes.has(scope);
  // The newest event applied; room.list gives the first value and the stream resumes after it.
  private head = 0;
  private stop = () => {};
  private readonly connect: typeof stream;
  constructor(connect: typeof stream = stream) { this.connect = connect; }

  async open(): Promise<void> {
    const [list, mine] = await Promise.all([call("room.list", {}), call("device.scopes", {}).catch((error: unknown) => { if (error instanceof APIError && error.detail.code === "not_found") return undefined; throw error; })]);
    if (mine) { this.scopes.clear(); for (const scope of mine.scopes ?? []) this.scopes.add(scope); }
    this.head = list.seq;
    for (const room of list.rooms ?? []) this.add(room);
    this.stop = this.connect(undefined, () => this.head, event => this.apply(event));
  }

  async created(id: string): Promise<void> {
    const list = await call("room.list", {});
    const room = list.rooms?.find(item => item.id === id);
    if (room && !this.rooms.has(id)) this.add(room);
  }

  private add(room: RoomSummary): void {
    this.rooms.set(room.id, room);
    this.waiting.set(room.id, new SvelteSet(room.waiting));
    this.working.set(room.id, new SvelteSet(room.working));
    this.broken.set(room.id, new SvelteMap());
    this.failed.set(room.id, new SvelteMap());
    this.hostless.set(room.id, new SvelteSet());
    if (room.failed || room.hostless) this.seedRoom(room.id);
    if (room.latest && !this.activity.has(room.latest.session)) this.activity.set(room.latest.session, room.latest.label);
  }

  // A room whose failures or host waits only its sessions name reads them until one read seeds it, with the client's
  // capped backoff, and not once the owner is signed out. The read that seeds covers every event up to its seq, so a
  // failed one loses nothing.
  private readonly seedTimers = new Map<string, ReturnType<typeof setTimeout>>();
  private seedRoom(room: string, tries = 0): void {
    this.listSessions(room).catch((error: unknown) => {
      if (blockedBy(error) !== "signed-out") this.seedTimers.set(room, setTimeout(() => { if (!this.sessions.has(room)) this.seedRoom(room, tries + 1); }, retryDelay(tries)));
    });
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

  close(): void { this.stop(); clearTimeout(this.timer); for (const timer of this.seedTimers.values()) clearTimeout(timer); }

  // The sessions that really work in a room.
  busy(room: string): string[] { return [...this.working.get(room) ?? []].filter(id => !this.broken.get(room)?.has(id) && !this.hostless.get(room)?.has(id)); }

  // A room's failed sessions, as RoomStore.status counts them: a broken box, or a failed turn the owner has not read
  // past. The rail mark and Home's Needs you and Active now all read this, so they clear together.
  down(room: string): { id: string; box?: BoxFailure; cause?: FailureCause }[] {
    const feed = this.rooms.get(room)?.lastRead ?? 0, broken = this.broken.get(room);
    const turns = [...this.failed.get(room) ?? []].filter(([id, { seq }]) => Math.max(feed, this.read.get(id) ?? 0) < seq && !broken?.has(id));
    return [...[...broken ?? []].map(([id, box]) => ({ id, box })), ...turns.map(([id, { cause }]) => ({ id, cause }))];
  }

  failing(room: string): number { return this.down(room).length; }

  // A room's sessions for Home, which seed the room's set once. Callers share one read until the room seeds, so only
  // that read owns the buffer. A failed one keeps no buffer.
  private readonly seeding = new Map<string, Promise<SessionListResponse>>();
  listSessions(room: string): Promise<SessionListResponse> {
    if (this.sessions.has(room)) return this.readSessions(room);
    const read = this.seeding.get(room) ?? this.readSessions(room).finally(() => this.seeding.delete(room));
    this.seeding.set(room, read);
    return read;
  }

  private readSessions(room: string): Promise<SessionListResponse> {
    if (!this.sessions.has(room)) this.pending.set(room, this.pending.get(room) ?? []);
    return call("session.list", { room }).then(async list => {
      for (const { id, lastRead } of list.sessions ?? []) if (lastRead > (this.read.get(id) ?? 0)) this.read.set(id, lastRead);
      if (!this.sessions.has(room)) {
        const failing = (list.sessions ?? []).filter(session => session.lastOutcome === "failed").map(session => session.id);
        const placed = await turnFailures(room, list.seq + 1, failing);
        for (const id of failing) this.failed.get(room)?.set(id, { seq: placed.get(id)?.seq ?? list.seq, cause: placed.get(id)?.body.cause });
      }
      this.seed(list.sessions ?? [], { id: room, seq: list.seq });
      const broken = this.broken.get(room);
      if (broken?.size) void boxFailures(room, list.seq + 1).then(found => {
        for (const [id, failure] of found) if (broken.has(id)) broken.set(id, failure);
      });
      return list;
    }, (error: unknown) => { this.pending.delete(room); throw error; });
  }

  // Labels are live only, so a session.list answer seeds the turn and label of a session already
  // working; a turn or label the stream sent first is newer and stays. With a room, the answer also seeds that
  // room's set once, and the buffered events after the answer's seq apply on top.
  seed(sessions: Pick<SessionSummary, "id" | "turn" | "activity" | "boxState" | "boxReason">[], room?: { id: string; seq: number }): void {
    if (room && !this.sessions.has(room.id)) {
      this.sessions.set(room.id, new SvelteSet(sessions.map(s => s.id)));
      this.broken.set(room.id, new SvelteMap(sessions.flatMap(({ id, boxState }) => boxState === "failed" || boxState === "lost" ? [[id, { box: boxState }]] : [])));
      this.hostless.set(room.id, new SvelteSet(sessions.flatMap(({ id, boxState, boxReason }) => pendingReason(boxState, boxReason) === "room-host" ? [id] : [])));
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
      if (this.working.get(event.room ?? "")?.has(session) && (this.turns.get(session) ?? turn) === turn) {
        this.activity.delete(session);
        this.activity.set(session, label);
      }
      return;
    }
    // A read position also has seq 0. The feed's moves only forward, so a room store opened later starts from it.
    if (event.type === "conversation.read") {
      const room = this.rooms.get(event.body.room);
      if (room && event.body.conversation.kind === "feed" && event.body.seq > room.lastRead) this.rooms.set(room.id, { ...room, lastRead: event.body.seq });
      const dm = event.body.conversation.session;
      if (dm && event.body.seq > (this.read.get(dm) ?? 0)) this.read.set(dm, event.body.seq);
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
      // Only owner-class requests wait for the owner.
      case "op.requested": if (event.body.class === "owner") waiting.add(event.body.id); break;
      case "op.decided": case "op.ran": if (event.body.state !== "pending") waiting.delete(event.body.id); break;
      case "room.paused": case "room.resumed": {
        const room = this.rooms.get(event.room ?? "");
        if (room) this.rooms.set(room.id, { ...room, paused: event.type === "room.paused" });
        break;
      }
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
    const broken = this.broken.get(event.room ?? "");
    if (event.type === "session.closed") broken?.delete(event.body.session);
    const failed = this.failed.get(event.room ?? "");
    if (event.type === "session.closed") failed?.delete(event.body.session);
    if (event.type === "session.closed") this.hostless.get(event.room ?? "")?.delete(event.body.session);
    // Only a completed turn clears the failure, as in RoomStore; a stopped turn did not succeed.
    if (event.type === "turn.ended" && event.body.outcome === "failed") failed?.set(event.body.session, { seq: event.seq, cause: event.body.cause });
    if (event.type === "turn.ended" && event.body.outcome === "completed") failed?.delete(event.body.session);
    if (event.type === "box.changed") {
      const { session, to, cause } = event.body;
      if (to === "failed" || to === "lost") broken?.set(session, { box: to, cause: cause ?? broken.get(session)?.cause, at: event.at }); else broken?.delete(session);
      if (pendingReason(to, event.body.reason) === "room-host") this.hostless.get(event.room ?? "")?.add(session); else this.hostless.get(event.room ?? "")?.delete(session);
    }
  }
}
