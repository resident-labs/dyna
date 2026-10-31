import { SvelteMap, createSubscriber } from "svelte/reactivity";
import { call } from "./client.ts";
import { decodeEvent } from "./event.ts";
import type { Conversation, Event, PostRequest } from "./contract.gen.ts";

// An owner's post the engine has not confirmed: seq is set once the post call returns, failed when it did not.
export type PendingPost = { id: number; request: PostRequest; at: string; seq?: number; failed: boolean };
export type PostEvent = Extract<Event, { type: "post.created" }>;

export const PAGE = 200;
export const WINDOW = 1000;

// The session whose DM holds an event, as the engine's events.list home filter reads it.
export function dmOf(event: Event): string | undefined {
  if (event.type === "post.created") return event.body.home.kind === "dm" ? event.body.home.session : undefined;
  if (event.type === "turn.ended") return event.body.replies?.find(({ reply }) => reply.home?.kind === "dm")?.reply.home?.session;
  return undefined;
}

// One open conversation's events: at most WINDOW of them, paged with beforeSeq and afterSeq.
// The feed window reads every room event; a DM window reads only the events whose home is that DM.
export class EventWindow {
  readonly events = new SvelteMap<number, Event>();
  readonly seqs: number[] = [];
  readonly pendingPosts = new SvelteMap<number, PendingPost>();
  private sent = 0;
  private present = true;
  private older = false;
  private loaded = false;
  // The page request in flight; a jump to the present waits for it instead of being dropped.
  private busy: Promise<void> | undefined;
  // The last load error, kept until a load succeeds, so the owner sees it instead of a silent gap.
  private failure: unknown;
  // The newest event of this conversation seen live or loaded.
  private latest = 0;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });

  readonly room: string;
  readonly home: Conversation;
  private readonly named: (names: Record<string, string>) => void;
  constructor(room: string, home: Conversation, named: (names: Record<string, string>) => void = () => {}) {
    this.room = room;
    this.home = home;
    this.named = named;
  }

  get newest(): number { return this.seqs.at(-1) ?? 0; }
  get oldest(): number { return this.seqs[0] ?? 0; }
  // seqs is a plain array; reading events.size makes Svelte readers see new seqs.
  get ordered(): Event[] { return this.events.size ? this.seqs.map(seq => this.events.get(seq)!) : []; }
  // True while the window ends at the newest event, so live events append to it.
  get atPresent(): boolean { this.subscribe(); return this.present; }
  // True once the first page has loaded, so an empty conversation is really empty.
  get ready(): boolean { this.subscribe(); return this.loaded; }
  get error(): unknown { this.subscribe(); return this.failure; }
  get hasOlder(): boolean { this.subscribe(); return this.older; }

  // Loads the newest page once after has settled; the room passes its list load.
  open(after: Promise<void> = Promise.resolve()): Promise<void> {
    return this.run(async () => { await after; await this.loadLatest(); this.loaded = true; });
  }

  apply(event: Event): void {
    if (this.home.kind === "dm" && dmOf(event) !== this.home.session) return;
    if (this.present) {
      if (!this.insert(event)) return;
      this.release("oldest");
    } else if (event.seq <= this.latest) return;
    this.latest = Math.max(this.latest, event.seq);
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

  // Loads older pages until the view has enough rows, the history ends, or another page
  // would release the newest end: paging the reader did not ask for never leaves the present.
  async fill(enough: () => Promise<boolean>): Promise<void> {
    while (this.older && this.failure === undefined && this.seqs.length + PAGE <= WINDOW && !(await enough())) {
      if (this.busy) await this.busy;
      else await this.loadOlder();
    }
  }

  async jumpToPresent(): Promise<void> {
    while (this.busy) await this.busy;
    if (!this.present) await this.run(() => this.loadLatest());
  }

  // Runs one load at a time. It never rejects: an error is kept in error for the view.
  private run(task: () => Promise<void>): Promise<void> {
    const busy = task().then(() => { this.failure = undefined; }, (error: unknown) => { this.failure = error; }).finally(() => {
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
  }

  private async page(range: { beforeSeq: number } | { afterSeq: number }): Promise<{ events: Event[]; full: boolean }> {
    const page = await call("events.list", {
      room: this.room, afterSeq: 0, ...range, limit: PAGE, home: this.home.kind === "dm" ? this.home : undefined,
    });
    const values = page.events ?? [];
    // Names of the page's sessions, closed ones too, because session.list returns open sessions only.
    this.named(page.names ?? {});
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
    // A sent post stays pending until its event is in the window, so it never disappears.
    for (const [id, post] of this.pendingPosts) {
      if (post.seq === event.seq) this.pendingPosts.delete(id);
    }
    return true;
  }

  posts(): PostEvent[] {
    return this.ordered.filter((event): event is PostEvent => event.type === "post.created" &&
      event.body.home.kind === this.home.kind && event.body.home.session === this.home.session);
  }

  // Sends a post; it shows as pending until its event is in the window, and a failed send stays for retry or discard.
  post(request: PostRequest): Promise<void> {
    const id = ++this.sent;
    this.pendingPosts.set(id, { id, request, at: new Date().toISOString(), failed: false });
    return this.retry(id);
  }

  async retry(id: number): Promise<void> {
    const known = this.pendingPosts.get(id);
    if (!known) return;
    const pending = { ...known, failed: false };
    this.pendingPosts.set(id, pending);
    // The owner's post shows at the present, so the window moves there while it sends.
    const jumped = this.jumpToPresent();
    // The row says "Not sent"; the error text is raw (r8).
    const response = await call("post", pending.request).catch(() => undefined);
    if (!response) this.pendingPosts.set(id, { ...pending, failed: true });
    else if (this.events.has(response.seq)) this.pendingPosts.delete(id);
    else this.pendingPosts.set(id, { ...pending, seq: response.seq });
    await jumped;
  }

  discard(id: number): void { this.pendingPosts.delete(id); }
}
