import { SvelteMap, SvelteSet } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import type { BoxState, Conversation, Event, OpRequested, Operation, PostRequest, SessionStarted } from "./contract.gen.ts";

export type PendingPost = { text: string; home: Conversation; seq?: number };
export type PendingOperation = OpRequested | Operation;

export class RoomStore {
  readonly events = new SvelteMap<number, Event>();
  readonly seqs: number[] = [];
  readonly pendingPosts = new SvelteSet<PendingPost>();
  readonly sessions = new SvelteMap<string, SessionStarted>();
  readonly boxes = new SvelteMap<string, BoxState>();
  readonly turns = new SvelteMap<string, string>();
  readonly operations = new SvelteMap<string, PendingOperation>();
  private initialOperations = new Map<string, PendingOperation>();
  private operationBaseline = 0;
  private sessionBaseline = 0;
  private initialSessions = new Map<string, SessionStarted>();
  private initialBoxes = new Map<string, BoxState>();
  private stopStream = () => {};
  private closed = false;

  readonly room: string;
  constructor(room: string) { this.room = room; }

  get newest(): number { return this.seqs.at(-1) ?? 0; }
  // seqs is a plain array; reading events.size makes Svelte readers see new seqs.
  get ordered(): Event[] { return this.events.size ? this.seqs.map(seq => this.events.get(seq)!) : []; }

  async open(): Promise<void> {
    const page = await call("events.list", { room: this.room, afterSeq: 0, beforeSeq: Number.MAX_SAFE_INTEGER, limit: 200 });
    for (const event of page.events ?? []) this.apply(event);
    const sessionList = await call("session.list", { room: this.room });
    this.sessionBaseline = this.newest;
    this.initialSessions = new Map((sessionList.sessions ?? []).map(session => [session.id, {
      session: session.id, name: session.name, parent: session.parent || undefined,
      profile: session.profile, goal: session.goal || undefined,
    }]));
    this.initialBoxes = new Map((sessionList.sessions ?? []).map(session => [session.id, session.boxState]));
    const listed = await call("op.list", { room: this.room });
    this.operationBaseline = this.newest;
    this.initialOperations = new Map((listed.operations ?? [])
      .filter(op => op.state === "pending").map(op => [op.id, op]));
    this.fold();
    if (this.closed) return;
    this.stopStream = stream(this.room, () => this.newest, event => this.apply(event));
  }

  close(): void { this.closed = true; this.stopStream(); }

  apply(event: Event): void {
    if (this.events.has(event.seq)) return;
    this.events.set(event.seq, event);
    const index = this.seqs.findIndex(seq => seq > event.seq);
    if (index < 0) this.seqs.push(event.seq);
    else this.seqs.splice(index, 0, event.seq);
    for (const post of this.pendingPosts) {
      if (post.seq === event.seq) this.pendingPosts.delete(post);
    }
    this.fold();
  }

  private fold(): void {
    const sessions = new Map(this.initialSessions);
    const boxes = new Map(this.initialBoxes);
    const turns = new Map<string, string>();
    const operations = new Map(this.initialOperations);
    for (const event of this.ordered) {
      switch (event.type) {
        case "session.started":
          if (event.seq > this.sessionBaseline) sessions.set(event.body.session, event.body);
          break;
        case "session.closed":
          if (event.seq > this.sessionBaseline) sessions.delete(event.body.session);
          break;
        case "box.changed":
          if (event.seq > this.sessionBaseline) boxes.set(event.body.session, event.body.to);
          break;
        case "turn.started": turns.set(event.body.session, event.body.turn); break;
        case "turn.ended":
          if (turns.get(event.body.session) === event.body.turn) turns.delete(event.body.session);
          break;
        case "op.requested":
          if (event.seq > this.operationBaseline) operations.set(event.body.id, event.body);
          break;
        case "op.decided": case "op.ran":
          if (event.seq > this.operationBaseline && event.body.state !== "pending") operations.delete(event.body.id);
          break;
      }
    }
    this.sessions.clear(); for (const entry of sessions) this.sessions.set(...entry);
    this.boxes.clear(); for (const entry of boxes) this.boxes.set(...entry);
    this.turns.clear(); for (const entry of turns) this.turns.set(...entry);
    this.operations.clear(); for (const entry of operations) this.operations.set(...entry);
  }

  posts(home: Conversation): Event[] {
    return this.ordered.filter(event => event.type === "post.created" &&
      event.body.home.kind === home.kind && event.body.home.session === home.session);
  }

  async post(request: PostRequest): Promise<void> {
    const pending: PendingPost = { text: request.text, home: request.home ?? { kind: "feed" } };
    this.pendingPosts.add(pending);
    try {
      const response = await call("post", request);
      pending.seq = response.seq;
      if (this.events.has(response.seq)) this.pendingPosts.delete(pending);
    } catch (error) {
      this.pendingPosts.delete(pending);
      throw error;
    }
  }
}
