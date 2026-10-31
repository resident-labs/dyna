import { createSubscriber } from "svelte/reactivity";
import type { PostSearchRequest, PostSearchResponse, SearchPost, SearchSession, SessionSearchRequest, SessionSearchResponse } from "./contract.gen.ts";

export type PostHit = SearchPost;
export type SearchRequest = PostSearchRequest;
export type SearchResponse = PostSearchResponse;
export type SearchCall = (request: SearchRequest) => Promise<SearchResponse>;

export class SessionSearch {
  private state: "idle" | "searching" | "failed" | "done" = "idle";
  private sessions: SearchSession[] = [];
  private total = 0;
  private generation = 0;
  private changed = () => {};
  private subscribe = createSubscriber(update => { this.changed = update; });
  private readonly search: (request: SessionSearchRequest) => Promise<SessionSearchResponse>;
  constructor(search: (request: SessionSearchRequest) => Promise<SessionSearchResponse>) { this.search = search; }
  get snapshot() { this.subscribe(); return { state: this.state, sessions: this.sessions, total: this.total }; }
  prepare(query: string): void {
    ++this.generation;
    this.sessions = [];
    this.total = 0;
    this.state = query ? "searching" : "idle";
    this.changed();
  }
  async start(query: string): Promise<void> {
    const generation = ++this.generation;
    if (!query) { this.state = "idle"; this.changed(); return; }
    this.state = "searching";
    this.changed();
    try {
      const result = await this.search({ query, limit: 50 });
      if (generation !== this.generation) return;
      this.sessions = result.sessions ?? [];
      this.total = result.total;
      this.state = "done";
    } catch {
      if (generation !== this.generation) return;
      this.state = "failed";
    }
    this.changed();
  }
}

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
