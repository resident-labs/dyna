import { SvelteMap, SvelteSet, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import { EventWindow } from "./window.svelte.ts";
import { countOf, statusOf, type Counts, type Status } from "./status.ts";
import type { BoxState, Conversation, Decision, Event, OpRequested, OpResult, OpState, SessionStarted } from "./contract.gen.ts";

// An operation of the room as the Requests panel shows it: its card, then its decision and run.
export type Request = OpRequested & { state: OpState; decision?: Decision; result?: OpResult };

// A read position's key: "" for the feed, the session id for its DM.
const readKey = (conversation: Conversation) => conversation.kind === "dm" ? conversation.session ?? "" : "";

// Sessions, boxes, turns and operations come from session.list and op.list once, then from
// live events, never from history. Each open conversation reads its events in its own window.
export class RoomStore {
  readonly sessions = new SvelteMap<string, SessionStarted>();
  readonly boxes = new SvelteMap<string, BoxState>();
  readonly turns = new SvelteMap<string, string>();
  // The running turn's latest activity label per session, such as "read spec.md".
  readonly activity = new SvelteMap<string, string>();
  // Sessions whose last turn failed, with that turn.ended's seq.
  readonly failed = new SvelteMap<string, number>();
  // The owner's read position per conversation, by readKey; the newest seq wins.
  private readonly read = new SvelteMap<string, number>();
  // Sessions whose box runs a computer the owner can watch.
  readonly computers = new SvelteSet<string>();
  // Background shell jobs still running when each session's last turn ended.
  readonly shellJobs = new SvelteMap<string, number>();
  // op.list gives the pending operations; decided ones stay for the time the room is open.
  readonly requests = new SvelteMap<string, Request>();
  // Names outlive closed sessions, so old posts still show who wrote them.
  readonly names = new SvelteMap<string, string>();
  private readonly windows = new Set<EventWindow>();
  // The newest event applied; session.list gives the first value and the stream resumes after it.
  private head = 0;
  // Windows load after the lists, so their first page holds every event the stream skips.
  private opening: Promise<void> = Promise.resolve();
  private loaded = false;
  private failure: unknown;
  private stopStream = () => {};
  private closed = false;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });

  readonly room: string;
  private readonly connect: typeof stream;
  constructor(room: string, connect: typeof stream = stream) {
    this.room = room;
    this.connect = connect;
  }

  // A failure marks its session until the owner has read past it, in the feed or in the session's DM.
  unseen(id: string): boolean {
    const at = this.failed.get(id);
    return at !== undefined && Math.max(this.read.get("") ?? 0, this.read.get(id) ?? 0) < at;
  }
  // Keeps a read position: room.list gives the feed's, session.list each DM's, and conversation.read events move them.
  noteRead(conversation: Conversation, seq: number): void {
    if (seq > (this.read.get(readKey(conversation)) ?? 0)) this.read.set(readKey(conversation), seq);
  }
  // The newest event the owner has read in a conversation, or 0.
  readAt(conversation: Conversation): number { return this.read.get(readKey(conversation)) ?? 0; }
  // The owner has seen a conversation up to seq; the engine keeps it for every device.
  see(conversation: Conversation, seq: number): void {
    if ((this.read.get(readKey(conversation)) ?? 0) >= seq) return;
    this.noteRead(conversation, seq);
    // A failed write leaves the mark clear on this page only; other devices keep it until the next read.
    void call("conversation.read", { room: this.room, conversation, seq }).catch(() => {});
  }

  // True once session.list and op.list have loaded.
  get ready(): boolean { this.subscribe(); return this.loaded; }
  get error(): unknown { this.subscribe(); return this.failure; }
  // Pending owner-class operations wait for the owner; agent-class ones wait for an ancestor session first.
  get waiting(): number { return [...this.requests.values()].filter(request => request.state === "pending" && request.class === "owner").length; }
  // Each session's one status (status.ts): a request it made waits for the owner, its failure is unseen or its box
  // failed, or a turn runs. The header, the panel and the counts read only these.
  status(id: string): Status { return this.statusWith(id, this.asking()); }
  get counts(): Counts { const asking = this.asking(); return countOf([...this.sessions.keys()].map(id => this.statusWith(id, asking))); }
  private statusWith(id: string, asking: Set<string>): Status {
    const box = this.boxes.get(id);
    return statusOf({ waiting: asking.has(id), failed: this.unseen(id) || box === "failed" || box === "lost", working: this.turns.has(id) });
  }
  // The sessions whose requests wait for the owner.
  private asking(): Set<string> {
    return new Set([...this.requests.values()].filter(request => request.state === "pending" && request.class === "owner")
      .flatMap(request => request.by.session ? [request.by.session] : []));
  }

  async open(): Promise<void> {
    this.opening = this.load();
    await this.opening.then(() => { this.loaded = true; }, (error: unknown) => { this.failure = error; });
    this.changed();
    if (this.closed || !this.loaded) return;
    this.stopStream = this.connect(this.room, () => this.head, event => this.apply(event));
  }

  private async load(): Promise<void> {
    const list = await call("session.list", { room: this.room });
    // op.list reads after the seq, so an operation it misses arrives as a live event.
    const listed = await call("op.list", { room: this.room });
    this.head = list.seq;
    for (const session of list.sessions ?? []) {
      this.sessions.set(session.id, {
        session: session.id, name: session.name, parent: session.parent || undefined,
        profile: session.profile, goal: session.goal || undefined,
      });
      this.names.set(session.id, session.name);
      this.noteRead({ kind: "dm", session: session.id }, session.lastRead);
      this.boxes.set(session.id, session.boxState);
      if (session.turn) this.turns.set(session.id, session.turn);
      if (session.turn && session.activity) this.activity.set(session.id, session.activity);
      if (session.lastOutcome === "failed") this.failed.set(session.id, list.seq);
      if (session.computer === "running") this.computers.add(session.id);
      this.shellJobs.set(session.id, session.shellJobs ?? 0);
    }
    // session.list says which last turns failed but not when, so the room reads turn.ended pages back from the newest
    // until it has placed each failure. One it cannot place, as when a read fails, keeps list.seq and shows.
    const unplaced = new Set(this.failed.keys());
    // At most 5 pages, so a long room never waits on its whole history.
    for (let before = list.seq + 1, pages = 0; unplaced.size && pages < 5; pages++) {
      const page = await call("events.list", { room: this.room, afterSeq: 0, beforeSeq: before, limit: 200, types: ["turn.ended"] })
        .catch(() => undefined);
      const events = page?.events ?? [];
      const found = new Map<string, number>();
      for (const event of events) {
        if (event.type === "turn.ended" && unplaced.has(event.body.session)) found.set(event.body.session, Math.max(found.get(event.body.session) ?? 0, event.seq));
      }
      // A session's newest turn.ended is its failure, since its last outcome failed.
      for (const [session, seq] of found) { this.failed.set(session, seq); unplaced.delete(session); }
      if (events.length < 200) break;
      before = Math.min(...events.map(event => event.seq));
    }
    for (const { id, kind, by, class: level, card, expires, state } of listed.operations ?? []) {
      this.requests.set(id, { id, kind, by, class: level, card, expires, state });
    }
  }

  close(): void { this.closed = true; this.stopStream(); }

  // Opens a window on one conversation; the view that opened it calls leave when it goes.
  window(home: Conversation): EventWindow {
    const opened = new EventWindow(this.room, home, names => {
      for (const [session, name] of Object.entries(names)) this.names.set(session, name);
    });
    this.windows.add(opened);
    void opened.open(this.opening);
    return opened;
  }

  leave(window: EventWindow): void { this.windows.delete(window); }

  apply(event: Event): void {
    // A read position has seq 0 and moves only forward.
    if (event.type === "conversation.read") { this.noteRead(event.body.conversation, event.body.seq); return; }
    // A live activity label has seq 0 and never reaches a window; a label of another turn is stale.
    if (event.type === "turn.activity") {
      if (this.turns.get(event.body.session) === event.body.turn) this.activity.set(event.body.session, event.body.label);
      return;
    }
    if (event.seq <= this.head) return;
    this.head = event.seq;
    this.step(event);
    for (const window of this.windows) window.apply(event);
  }

  private step(event: Event): void {
    switch (event.type) {
      case "session.started":
        this.sessions.set(event.body.session, event.body);
        this.names.set(event.body.session, event.body.name);
        break;
      case "session.closed":
        this.sessions.delete(event.body.session);
        this.activity.delete(event.body.session);
        break;
      case "session.moved": {
        const moved = this.sessions.get(event.body.session);
        if (moved) this.sessions.set(event.body.session, { ...moved, parent: event.body.newParent });
        break;
      }
      case "box.changed":
        this.boxes.set(event.body.session, event.body.to);
        // A computer runs only inside a ready box.
        if (event.body.to !== "ready") this.computers.delete(event.body.session);
        break;
      case "computer.changed":
        if (event.body.to === "running") this.computers.add(event.body.session);
        else this.computers.delete(event.body.session);
        break;
      case "turn.started":
        this.turns.set(event.body.session, event.body.turn);
        this.activity.delete(event.body.session);
        break;
      case "turn.ended":
        if (this.turns.get(event.body.session) === event.body.turn) {
          this.turns.delete(event.body.session);
          this.activity.delete(event.body.session);
        }
        // Only a completed turn clears the failure; a stopped turn did not succeed.
        if (event.body.outcome === "failed") this.failed.set(event.body.session, event.seq);
        else if (event.body.outcome === "completed") this.failed.delete(event.body.session);
        this.shellJobs.set(event.body.session, event.body.shellJobs ?? 0);
        break;
      case "op.requested": this.requests.set(event.body.id, { ...event.body, state: "pending" }); break;
      case "op.decided": case "op.ran": {
        const known = this.requests.get(event.body.id);
        if (known) this.requests.set(event.body.id, { ...known, ...event.body });
        break;
      }
    }
  }
}
