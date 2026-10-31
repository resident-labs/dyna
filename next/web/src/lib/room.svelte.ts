import { SvelteMap, SvelteSet, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import { EventWindow } from "./window.svelte.ts";
import type { BoxState, Conversation, Decision, Event, OpRequested, OpResult, OpState, SessionStarted } from "./contract.gen.ts";

// An operation of the room as the Requests panel shows it: its card, then its decision and run.
export type Request = OpRequested & { state: OpState; decision?: Decision; result?: OpResult };
// Where the browser keeps the failures the owner has seen; the page passes localStorage.
export type SeenStorage = Pick<Storage, "getItem" | "setItem">;

// Sessions, boxes, turns and operations come from session.list and op.list once, then from
// live events, never from history. Each open conversation reads its events in its own window.
export class RoomStore {
  readonly sessions = new SvelteMap<string, SessionStarted>();
  readonly boxes = new SvelteMap<string, BoxState>();
  readonly turns = new SvelteMap<string, string>();
  // The running turn's latest activity label per session, such as "read spec.md".
  readonly activity = new SvelteMap<string, string>();
  // Sessions whose last turn failed, with that turn.ended's seq; 0 when the failure is older than load reads.
  readonly failed = new SvelteMap<string, number>();
  // Per session, the newest failure seq the owner has seen. The engine keeps no read position yet, so this lives
  // in the browser, and a failure seen on one device still shows on another.
  private readonly seen = new SvelteMap<string, number>();
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
  private readonly storage?: SeenStorage;
  constructor(room: string, connect: typeof stream = stream, storage?: SeenStorage) {
    this.room = room;
    this.connect = connect;
    this.storage = storage;
    const saved: unknown = JSON.parse(storage?.getItem(this.seenKey) ?? "{}");
    for (const [id, seq] of Object.entries(saved ?? {})) if (typeof seq === "number") this.seen.set(id, seq);
  }

  private get seenKey(): string { return `dyna.seen-failures.${this.room}`; }
  // A failure marks its session until the owner sees it: opens its DM or scrolls its row into view.
  unseen(id: string): boolean { const at = this.failed.get(id); return at !== undefined && (this.seen.get(id) ?? -1) < at; }
  get unseenCount(): number { return [...this.failed.keys()].filter(id => this.unseen(id)).length; }
  see(id: string, seq = this.failed.get(id) ?? 0): void {
    if ((this.seen.get(id) ?? -1) >= seq) return;
    this.seen.set(id, seq);
    this.storage?.setItem(this.seenKey, JSON.stringify(Object.fromEntries(this.seen)));
  }

  // True once session.list and op.list have loaded.
  get ready(): boolean { this.subscribe(); return this.loaded; }
  get error(): unknown { this.subscribe(); return this.failure; }
  // Pending owner-class operations wait for the owner; agent-class ones wait for an ancestor session first.
  get waiting(): number { return [...this.requests.values()].filter(request => request.state === "pending" && request.class === "owner").length; }

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
      this.boxes.set(session.id, session.boxState);
      if (session.turn) this.turns.set(session.id, session.turn);
      if (session.turn && session.activity) this.activity.set(session.id, session.activity);
      if (session.lastOutcome === "failed") this.failed.set(session.id, 0);
      if (session.computer === "running") this.computers.add(session.id);
      this.shellJobs.set(session.id, session.shellJobs ?? 0);
    }
    // session.list says which last turns failed but not when; the newest turn.ended events give each failure's seq.
    if (this.failed.size) {
      const ended = await call("events.list", { room: this.room, afterSeq: 0, beforeSeq: list.seq + 1, limit: 200, types: ["turn.ended"] });
      for (const event of ended.events ?? []) {
        const at = event.type === "turn.ended" && event.body.outcome === "failed" ? this.failed.get(event.body.session) : undefined;
        if (event.type === "turn.ended" && at !== undefined && event.seq > at) this.failed.set(event.body.session, event.seq);
      }
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
