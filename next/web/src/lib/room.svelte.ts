import { SvelteMap, SvelteSet, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { stream } from "./stream.ts";
import { decodeEvent } from "./event.ts";
import type { BoxState, Conversation, Event, OpRequested, Operation, PostRequest, SessionStarted } from "./contract.gen.ts";

export type PendingPost = { text: string; home: Conversation; seq?: number };
export type PendingOperation = OpRequested | Operation;
export type PostEvent = Extract<Event, { type: "post.created" }>;
type Folded = {
  sessions: Map<string, SessionStarted>; boxes: Map<string, BoxState>; turns: Map<string, string>;
  failed: Set<string>; operations: Map<string, PendingOperation>;
};

export const PAGE = 200;
export const WINDOW = 1000;

// The store holds a window of at most WINDOW events. Sessions, boxes, turns and
// operations come from session.list and op.list plus live events, never from history.
export class RoomStore {
  readonly events = new SvelteMap<number, Event>();
  readonly seqs: number[] = [];
  readonly pendingPosts = new SvelteSet<PendingPost>();
  readonly sessions = new SvelteMap<string, SessionStarted>();
  readonly boxes = new SvelteMap<string, BoxState>();
  readonly turns = new SvelteMap<string, string>();
  readonly failed = new SvelteSet<string>();
  readonly operations = new SvelteMap<string, PendingOperation>();
  // Names outlive closed sessions, so old posts still show who wrote them.
  readonly names = new SvelteMap<string, string>();
  private initialOperations = new Map<string, PendingOperation>();
  private operationBaseline = 0;
  private sessionBaseline = 0;
  private initialSessions = new Map<string, SessionStarted>();
  private initialBoxes = new Map<string, BoxState>();
  private present = true;
  private older = false;
  private loaded = false;
  // The page request in flight; a jump to the present waits for it instead of being dropped.
  private busy: Promise<void> | undefined;
  private latest = 0;
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

  get newest(): number { return this.seqs.at(-1) ?? 0; }
  get oldest(): number { return this.seqs[0] ?? 0; }
  // seqs is a plain array; reading events.size makes Svelte readers see new seqs.
  get ordered(): Event[] { return this.events.size ? this.seqs.map(seq => this.events.get(seq)!) : []; }
  // True while the window ends at the newest event, so live events append to it.
  get atPresent(): boolean { this.subscribe(); return this.present; }
  // True once the first page has loaded, so an empty feed is really empty.
  get ready(): boolean { this.subscribe(); return this.loaded; }

  async open(): Promise<void> {
    await this.run(() => this.loadLatest());
    const sessionList = await call("session.list", { room: this.room });
    this.sessionBaseline = this.latest;
    this.initialSessions = new Map((sessionList.sessions ?? []).map(session => [session.id, {
      session: session.id, name: session.name, parent: session.parent || undefined,
      profile: session.profile, goal: session.goal || undefined,
    }]));
    for (const session of this.initialSessions.values()) this.names.set(session.session, session.name);
    this.initialBoxes = new Map((sessionList.sessions ?? []).map(session => [session.id, session.boxState]));
    const listed = await call("op.list", { room: this.room });
    this.operationBaseline = this.latest;
    this.initialOperations = new Map((listed.operations ?? [])
      .filter(op => op.state === "pending").map(op => [op.id, op]));
    this.fold();
    if (this.closed) return;
    this.stopStream = this.connect(this.room, () => this.latest, event => this.apply(event));
  }

  close(): void { this.closed = true; this.stopStream(); }

  apply(event: Event): void {
    if (this.present) {
      if (!this.insert(event)) return;
      this.release("oldest");
    } else if (event.seq <= this.latest) return;
    this.latest = Math.max(this.latest, event.seq);
    this.step(event, this);
  }

  // Loads the page before the window and releases the newest end beyond WINDOW.
  async loadOlder(): Promise<void> {
    if (this.busy || !this.older) return;
    await this.run(async () => {
      const page = await this.page({ beforeSeq: this.oldest });
      for (const event of page.events) this.insert(event);
      this.older = page.full;
      this.release("newest");
    });
  }

  // Loads the page after the window, fetched again by its boundary seq, until it reaches the newest event.
  async loadNewer(): Promise<void> {
    if (this.busy || this.present) return;
    await this.run(async () => {
      const page = await this.page({ afterSeq: this.newest });
      for (const event of page.events) this.insert(event);
      this.release("oldest");
      if (!page.full && this.newest >= this.latest) this.present = true;
    });
  }

  async jumpToPresent(): Promise<void> {
    while (this.busy) await this.busy.catch(() => {});
    if (!this.present) await this.run(() => this.loadLatest());
  }

  private run(task: () => Promise<void>): Promise<void> {
    const busy = task().finally(() => {
      if (this.busy === busy) this.busy = undefined;
      this.changed();
    });
    this.busy = busy;
    return busy;
  }

  private async loadLatest(): Promise<void> {
    const page = await this.page({ beforeSeq: Number.MAX_SAFE_INTEGER });
    this.events.clear();
    this.seqs.length = 0;
    for (const event of page.events) this.insert(event);
    this.older = page.full;
    this.latest = Math.max(this.latest, this.newest);
    // Live events that arrived during the request are fetched before the window follows them again.
    while (this.newest < this.latest) {
      const more = await this.page({ afterSeq: this.newest });
      if (!more.events.length) break;
      for (const event of more.events) this.insert(event);
    }
    this.release("oldest");
    this.present = true;
    this.loaded = true;
  }

  private async page(range: { beforeSeq: number } | { afterSeq: number }): Promise<{ events: Event[]; full: boolean }> {
    const page = await call("events.list", { room: this.room, afterSeq: 0, ...range, limit: PAGE });
    const values = page.events ?? [];
    return { events: values.map(decodeEvent).filter(event => event !== undefined), full: values.length === PAGE };
  }

  private release(end: "oldest" | "newest"): void {
    while (this.seqs.length > WINDOW) {
      const seq = end === "oldest" ? this.seqs.shift() : this.seqs.pop();
      if (seq !== undefined) this.events.delete(seq);
      if (end === "oldest") this.older = true;
      else this.present = false;
    }
  }

  private insert(event: Event): boolean {
    if (this.events.has(event.seq)) return false;
    this.events.set(event.seq, event);
    const index = this.seqs.findIndex(seq => seq > event.seq);
    if (index < 0) this.seqs.push(event.seq);
    else this.seqs.splice(index, 0, event.seq);
    if (event.type === "session.started") this.names.set(event.body.session, event.body.name);
    // A sent post stays pending until its event is in the window, so it never disappears.
    for (const post of this.pendingPosts) {
      if (post.seq === event.seq) this.pendingPosts.delete(post);
    }
    return true;
  }

  private step(event: Event, state: Folded): void {
    switch (event.type) {
      case "session.started":
        if (event.seq > this.sessionBaseline) state.sessions.set(event.body.session, event.body);
        break;
      case "session.closed":
        if (event.seq > this.sessionBaseline) state.sessions.delete(event.body.session);
        break;
      case "box.changed":
        if (event.seq > this.sessionBaseline) state.boxes.set(event.body.session, event.body.to);
        break;
      case "turn.started": state.turns.set(event.body.session, event.body.turn); break;
      case "turn.ended":
        if (state.turns.get(event.body.session) === event.body.turn) state.turns.delete(event.body.session);
        if (event.body.outcome === "failed") state.failed.add(event.body.session);
        else state.failed.delete(event.body.session);
        break;
      case "op.requested":
        if (event.seq > this.operationBaseline) state.operations.set(event.body.id, event.body);
        break;
      case "op.decided": case "op.ran":
        if (event.seq > this.operationBaseline && event.body.state !== "pending") state.operations.delete(event.body.id);
        break;
    }
  }

  // Folds the first page once on open; turns and failures exist only as events.
  private fold(): void {
    const state: Folded = {
      sessions: new Map(this.initialSessions), boxes: new Map(this.initialBoxes),
      turns: new Map(), failed: new Set(), operations: new Map(this.initialOperations),
    };
    for (const event of this.ordered) this.step(event, state);
    this.sessions.clear(); for (const entry of state.sessions) this.sessions.set(...entry);
    this.boxes.clear(); for (const entry of state.boxes) this.boxes.set(...entry);
    this.turns.clear(); for (const entry of state.turns) this.turns.set(...entry);
    this.failed.clear(); for (const session of state.failed) this.failed.add(session);
    this.operations.clear(); for (const entry of state.operations) this.operations.set(...entry);
  }

  posts(home: Conversation): PostEvent[] {
    return this.ordered.filter((event): event is PostEvent => event.type === "post.created" &&
      event.body.home.kind === home.kind && event.body.home.session === home.session);
  }

  async post(request: PostRequest): Promise<void> {
    const pending: PendingPost = { text: request.text, home: request.home ?? { kind: "feed" } };
    this.pendingPosts.add(pending);
    // The owner's post shows at the present, so the window moves there while it sends.
    const jumped = this.jumpToPresent();
    const response = await call("post", request)
      .catch((error: unknown) => { this.pendingPosts.delete(pending); throw error; });
    pending.seq = response.seq;
    if (this.events.has(response.seq)) this.pendingPosts.delete(pending);
    await jumped;
  }
}
