import { createSubscriber } from "svelte/reactivity";
import type { Conversation, Principal } from "./contract.gen.ts";

// Temporary boundary until post.search joins the generated contract.
export type PostHit = { seq: number; room: string; home: Conversation; author: Principal; time: string; text: string };
export type SearchRequest = { query: string; room?: string; before?: number; limit?: number };
export type SearchResponse = { posts: PostHit[] | null; total: number; names: Record<string, string> | null };
export type SearchCall = (request: SearchRequest) => Promise<SearchResponse>;

export class MessageSearch {
  state: "idle" | "searching" | "failed" | "done" = "idle";
  posts: PostHit[] = [];
  total = 0;
  names: Record<string, string> = {};
  private generation = 0;
  private query = "";
  private room: string | undefined;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });
  private readonly search: SearchCall;
  constructor(search: SearchCall) { this.search = search; }
  get snapshot() {
    this.subscribe();
    return { state: this.state, posts: this.posts, total: this.total };
  }
  prepare(query: string): void {
    ++this.generation;
    this.posts = [];
    this.total = 0;
    this.names = {};
    this.state = query ? "searching" : "idle";
    this.changed();
  }
  async start(query: string, room?: string): Promise<void> {
    const generation = ++this.generation;
    this.query = query;
    this.room = room;
    this.posts = [];
    this.total = 0;
    this.names = {};
    if (!query) { this.state = "idle"; this.changed(); return; }
    this.state = "searching";
    this.changed();
    await this.request(generation);
  }
  async more(): Promise<void> {
    if (this.state !== "done" || this.posts.length >= this.total || !this.posts.length) return;
    this.state = "searching";
    this.changed();
    await this.request(this.generation, this.posts.at(-1)?.seq);
  }
  private async request(generation: number, before?: number): Promise<void> {
    try {
      const result = await this.search({ query: this.query, room: this.room, before, limit: 50 });
      if (generation !== this.generation) return;
      this.posts = before === undefined ? result.posts ?? [] : [...this.posts, ...result.posts ?? []];
      this.total = result.total;
      this.names = { ...this.names, ...result.names };
      this.state = "done";
    } catch {
      if (generation !== this.generation) return;
      this.state = "failed";
    }
    this.changed();
  }
}
