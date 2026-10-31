import { SvelteMap, SvelteSet, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import { EventWindow } from "./window.svelte.ts";
import type { BoxState, Conversation, Event, OpRequested, Operation, SessionStarted } from "./contract.gen.ts";

export type PendingOperation = OpRequested | Operation;

// Sessions, boxes, turns and operations come from session.list and op.list once, then from
// live events, never from history. Each open conversation reads its events in its own window.
export class RoomStore {
  readonly sessions = new SvelteMap<string, SessionStarted>();
  readonly boxes = new SvelteMap<string, BoxState>();
  readonly turns = new SvelteMap<string, string>();
  readonly failed = new SvelteSet<string>();
  readonly operations = new SvelteMap<string, PendingOperation>();
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

  // True once session.list and op.list have loaded.
  get ready(): boolean { this.subscribe(); return this.loaded; }
  get error(): unknown { this.subscribe(); return this.failure; }

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
      if (session.lastOutcome === "failed") this.failed.add(session.id);
    }
    for (const op of listed.operations ?? []) if (op.state === "pending") this.operations.set(op.id, op);
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
      case "session.closed": this.sessions.delete(event.body.session); break;
      case "box.changed": this.boxes.set(event.body.session, event.body.to); break;
      case "turn.started": this.turns.set(event.body.session, event.body.turn); break;
      case "turn.ended":
        if (this.turns.get(event.body.session) === event.body.turn) this.turns.delete(event.body.session);
        if (event.body.outcome === "failed") this.failed.add(event.body.session);
        else this.failed.delete(event.body.session);
        break;
      case "op.requested": this.operations.set(event.body.id, event.body); break;
      case "op.decided": case "op.ran":
        if (event.body.state !== "pending") this.operations.delete(event.body.id);
        break;
    }
  }
}
