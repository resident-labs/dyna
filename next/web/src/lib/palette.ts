import type { PostHit } from "./search.svelte.ts";

// The Ctrl+K palette's rules, apart from the screen: matching, the grouped list, the keys and the time labels.

export type Mark = { text: string; hit: boolean };

const wordsOf = (query: string) => query.toLowerCase().split(/\s+/).filter(Boolean);
// Where each word of the text begins: after anything that is not a letter or a digit, so "auto-deploy" has two.
const begins = (lower: string) => [...lower.matchAll(/(?<![\p{L}\p{N}])[\p{L}\p{N}]/gu)].map(found => found.index);

// Every query word must start a word of the text. Case is ignored.
export function matches(query: string, text: string): boolean {
  const lower = text.toLowerCase(), at = begins(lower);
  return wordsOf(query).every(word => at.some(index => lower.startsWith(word, index)));
}

// Where the earliest matched word starts, so a name that starts with the query sorts above one that only contains it.
const earliest = (query: string, text: string) => {
  const lower = text.toLowerCase(), words = wordsOf(query);
  return begins(lower).find(index => words.some(word => lower.startsWith(word, index))) ?? Infinity;
};

// The text cut into marked and plain parts. A mark covers only the matched part of a word. A long text starts a little
// before its first match, so the match is never lost behind the clamp.
export function marks(text: string, query: string, lead = 48): Mark[] {
  const lower = text.toLowerCase(), words = wordsOf(query);
  const ranges: [number, number][] = [];
  for (const index of begins(lower)) {
    const longest = Math.max(0, ...words.filter(word => lower.startsWith(word, index)).map(word => word.length));
    if (!longest) continue;
    const last = ranges.at(-1);
    if (last && index <= last[1]) last[1] = Math.max(last[1], index + longest);
    else ranges.push([index, index + longest]);
  }
  const first = ranges[0]?.[0] ?? 0;
  const start = first > lead ? begins(lower).findLast(index => index <= first - lead) ?? first - lead : 0;
  const parts: Mark[] = start ? [{ text: "…", hit: false }] : [];
  let at = start;
  for (const [from, to] of ranges) {
    if (from > at) parts.push({ text: text.slice(at, from), hit: false });
    parts.push({ text: text.slice(from, to), hit: true });
    at = to;
  }
  if (at < text.length) parts.push({ text: text.slice(at), hit: false });
  return parts;
}

export type Scope = "rooms" | "sessions" | "messages" | "actions";
export type Group = Scope | "recent";
export const titles: Record<Group, string> = { recent: "Recent", rooms: "Rooms", sessions: "Sessions", messages: "Messages", actions: "Actions" };
const nouns: Record<Scope, string> = { rooms: "rooms", sessions: "sessions", messages: "messages", actions: "actions" };
// A group of the mixed list shows this many rows, then its Show all row.
export const CAP = 3;

export type Place = { id: string; name: string; paused: boolean; sessions?: number; working: number };
export type Person = { id: string; room: string; roomName: string; name: string; root: boolean; closed: boolean; working: boolean };
export type ActionId = "requests" | "pause" | "theme" | "settings" | "new-session" | "new-room";
export type Action = { id: ActionId; label: string; hint: string; keys: string[]; icon: "inbox" | "pause" | "palette" | "gear" | "plus" };

export type Item =
  | { kind: "room"; room: Place }
  | { kind: "session"; session: Person }
  | { kind: "message"; post: PostHit }
  | { kind: "action"; action: Action }
  | { kind: "more"; scope: Scope; total: number };
export type Section = { group: Group; title: string; shown: number; total: number; items: Item[]; state?: "searching" | "failed" };

export type Messages = { state: "idle" | "searching" | "failed" | "done"; posts: PostHit[]; total: number };
export type Sources = { rooms: Place[]; sessions: Person[]; sessionsTotal?: number; sessionsState?: "searching" | "failed"; actions: Action[]; recent: Item[]; messages: Messages };

// A leading ">" asks for actions only, as in VS Code; the rest of the text filters them.
export function parse(text: string, scope?: Scope): { scope?: Scope; query: string } {
  if (!scope && text.trimStart().startsWith(">")) return { scope: "actions", query: text.trimStart().slice(1).trim() };
  return { scope, query: text.trim() };
}

// Open ones first, then the closer matches, then the order the source gave.
const ranked = <T extends { name: string }>(list: T[], query: string, closed: (item: T) => boolean) =>
  list.filter(item => matches(query, item.name))
    .map((item, order) => ({ item, order, early: earliest(query, item.name), closed: closed(item) }))
    .sort((a, b) => Number(a.closed) - Number(b.closed) || a.early - b.early || a.order - b.order)
    .map(({ item }) => item);

export function build(text: string, scope: Scope | undefined, sources: Sources): Section[] {
  const mode = parse(text, scope), { query } = mode;
  const action = (list: Action[]) => query ? list.filter(item => matches(query, item.label)) : list;
  const section = (group: Scope, found: Item[], total = found.length, state?: Section["state"]): Section => {
    const alone = mode.scope === group;
    // The empty query lists every common action; a typed query caps each group at three.
    const cap = alone || (group === "actions" && !query) ? Infinity : CAP;
    const items = found.slice(0, cap);
    return { group, title: titles[group], shown: items.length, total, state,
      items: !alone && total > items.length && state !== "searching" ? [...items, { kind: "more", scope: group, total }] : items };
  };
  const rooms = section("rooms", ranked(sources.rooms, query, () => false).map((room): Item => ({ kind: "room", room })));
  const foundSessions = (sources.sessionsTotal === undefined ? ranked(sources.sessions, query, item => item.closed) : sources.sessions)
    .map((session): Item => ({ kind: "session", session }));
  const sessions = section("sessions", foundSessions.slice(0, 50), sources.sessionsTotal ?? foundSessions.length, sources.sessionsState);
  const messages = sources.messages;
  const post = section("messages", messages.posts.map((found): Item => ({ kind: "message", post: found })), Math.max(messages.total, messages.posts.length),
    messages.state === "searching" || messages.state === "failed" ? messages.state : undefined);
  const actions = section("actions", action(sources.actions).map((found): Item => ({ kind: "action", action: found })));
  if (mode.scope) return [{ rooms, sessions, messages: post, actions }[mode.scope]].filter(found => found.items.length || found.state);
  if (!query) {
    const recent: Section = { group: "recent", title: titles.recent, shown: sources.recent.length, total: sources.recent.length, items: sources.recent };
    return [recent, actions].filter(found => found.items.length);
  }
  return [rooms, sessions, post, actions].filter(found => found.items.length || found.state);
}

// The rows the arrow keys visit, in order.
export const rows = (sections: Section[]): Item[] => sections.flatMap(found => found.items);

// What to show instead of an empty list: nothing while a search runs or failed, else the one muted line.
export const nothing = (sections: Section[], query: string): string | undefined =>
  query && !sections.length ? `No room, session, message or action matches “${query}”.` : undefined;

// The count beside a group's title: "3 of 61" when the group shows part of it.
export const count = (found: Section): string | undefined => found.total > found.shown && found.group !== "recent" ? `${found.shown.toLocaleString()} of ${found.total.toLocaleString()}` : undefined;
export const showAll = (scope: Scope, total: number) => scope === "sessions" && total > 50
  ? `Show first 50 of ${total.toLocaleString()} sessions` : `Show all ${total.toLocaleString()} ${nouns[scope]}`;

export type Mode = { scope?: Scope; text: string };
// Esc leaves a scope and keeps the query; with no scope it closes.
export function escape(mode: Mode): { mode: Mode; close: boolean } {
  return mode.scope ? { mode: { text: mode.text }, close: false } : { mode, close: true };
}
// Backspace on an empty query leaves the scope; anywhere else it edits the text.
export const backspace = (mode: Mode): Mode | undefined => mode.scope && !mode.text ? { text: "" } : undefined;

// The arrow keys stop at the first and last row.
export const move = (pick: number, size: number, key: "ArrowDown" | "ArrowUp"): number =>
  size ? Math.min(size - 1, Math.max(0, pick + (key === "ArrowDown" ? 1 : -1))) : 0;

// The time on a message row: the clock today, else the day and the clock, else the date and the clock. Never relative.
export function stamp(value: string, now = Date.now(), locale: Intl.LocalesArgument = []): string {
  const at = new Date(value), today = new Date(now);
  const clock = at.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" });
  const days = Math.round((new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime()
    - new Date(at.getFullYear(), at.getMonth(), at.getDate()).getTime()) / 86_400_000);
  if (days === 0) return clock;
  if (days === 1) return `Yesterday ${clock}`;
  if (days > 1 && days < 7) return `${at.toLocaleDateString(locale, { weekday: "short" })} ${clock}`;
  const year = at.getFullYear() !== today.getFullYear() ? { year: "numeric" } as const : {};
  return `${at.toLocaleDateString(locale, { month: "short", day: "numeric", ...year })}, ${clock}`;
}

// The rooms and DMs the owner opened last, newest first. The stored text is the one unknown value here.
export type Visit = { room: string; session?: string };
const LIMIT = 6;
export function remember(list: Visit[], visit: Visit): Visit[] {
  return [visit, ...list.filter(old => old.room !== visit.room || old.session !== visit.session)].slice(0, LIMIT);
}
export function visits(stored: string | null): Visit[] {
  let parsed: unknown;
  try { parsed = stored ? JSON.parse(stored) : []; } catch { return []; }
  if (!Array.isArray(parsed)) return [];
  return parsed.flatMap((entry: unknown): Visit[] => {
    if (typeof entry !== "object" || entry === null || !("room" in entry) || typeof entry.room !== "string") return [];
    return "session" in entry && typeof entry.session === "string" ? [{ room: entry.room, session: entry.session }] : [{ room: entry.room }];
  }).slice(0, LIMIT);
}
