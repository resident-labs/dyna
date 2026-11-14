import { connectVersion, recoverConnect, type Renewal } from "./connect-watch.ts";
import type { Event, StreamHello } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export interface EventConnection {
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  addEventListener(type: "hello" | "heartbeat", listener: (event: MessageEvent<string>) => void): void;
  close(): void;
}
const browserEvents = (url: string) => new EventSource(url);
// EventSource hides HTTP refusals. Probe only after an error, then release the
// response body before reconnecting the real stream from its current cursor.
export async function recoverEventStream(url: string, version: number, signal?: AbortSignal, timeoutMs = 10_000): Promise<Renewal | undefined> {
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), timeoutMs);
  const bounded = signal ? AbortSignal.any([signal, timeout.signal]) : timeout.signal;
  try {
    const response = await fetch(url, { credentials: "same-origin", signal: bounded }).catch(() => undefined);
    if (!response) return "unavailable";
    // Cancelling this probe only stops its wait. Other callers still share the
    // separately bounded renewal, so the probe must not abort that operation.
    let aborted!: () => void;
    const cancelled = new Promise<"unavailable">(resolve => { aborted = () => resolve("unavailable"); });
    bounded.addEventListener("abort", aborted, { once: true });
    if (bounded.aborted) aborted();
    let result: Renewal | undefined;
    try { result = await Promise.race([recoverConnect(response, version), cancelled]); }
    finally { bounded.removeEventListener("abort", aborted); }
    await response.body?.cancel().catch(() => {});
    return result;
  } finally { clearTimeout(timer); }
}
export type EventFactory = (url: string) => EventConnection;
export type Schedule = (callback: () => void, delay: number) => () => void;
export type StreamOpened = (reconnect?: boolean, hello?: StreamHello) => void;
// The data stores subscribe logically; transport factory/timer injection belongs to their connection owner.
export type StreamSubscribe = (room: string | undefined, after: () => number, apply: (event: Event) => void,
  factory?: undefined, schedule?: undefined, opened?: StreamOpened) => () => void;

const transcriptListeners = new Set<(session?: string) => void>();
export function watchTranscripts(listener: (session?: string) => void): () => void {
  transcriptListeners.add(listener);
  return () => { transcriptListeners.delete(listener); };
}

// Each listener keeps its place, so a later watcher never replaces an earlier one.
const streamStateListeners = new Set<(up: boolean) => void>();
export function watchStreamState(listener: (up: boolean) => void): void { streamStateListeners.add(listener); }
function reportStreamState(up: boolean) { for (const listener of streamStateListeners) listener(up); }
let onHello: (hello: StreamHello) => void = () => {};
export function watchHello(listener: (hello: StreamHello) => void): void { onHello = listener; }

// Without a room, the owner stream carries the attention events of every room.
export function stream(
  room: string | undefined, after: () => number, apply: (event: Event) => void,
  factory: EventFactory = browserEvents,
  schedule: Schedule = (callback, delay) => {
    const id = setTimeout(callback, delay);
    return () => clearTimeout(id);
  },
  opened: StreamOpened = () => {},
  lifecycle: Pick<EventTarget, "addEventListener" | "removeEventListener"> | undefined = typeof window === "undefined" ? undefined : window,
  attentionAfter?: () => number,
): () => void {
  const owner = !room || attentionAfter !== undefined;
  let source: EventConnection;
  let probe: AbortController | undefined;
  let cancel = () => {};
  let closed = false;
  let delay = 500;
  let attempted = false;
  let suspended = false;
  let generation = 0;
  function connect() {
    const sentVersion = connectVersion();
    const currentGeneration = ++generation;
    const reconnect = attempted;
    attempted = true;
    const current = source = factory(`/v1/events?${new URLSearchParams({ ...(room && { room }),
      after: String(attentionAfter ? attentionAfter() : after()), ...(room && attentionAfter ? { roomAfter: String(after()) } : {}) })}`);
    let active = true;
    let heartbeatMs: number | undefined;
    let cancelWatchdog = () => {};
    const alive = () => !closed && !suspended && active && source === current && generation === currentGeneration;
    const refresh = () => {
      cancelWatchdog();
      if (heartbeatMs !== undefined) cancelWatchdog = schedule(failed, heartbeatMs * 2 + 5000);
    };
    function failed() {
      if (!alive()) return;
      active = false;
      cancelWatchdog();
      current.close();
      if (owner) reportStreamState(false);
      cancel = schedule(() => {
        const resume = () => { if (!closed && !suspended && generation === currentGeneration) connect(); };
        if (factory !== browserEvents) { resume(); return; }
        probe = new AbortController();
        void recoverEventStream(`/v1/events?${new URLSearchParams({ ...(room && { room }), after: String(after()) })}`, sentVersion, probe.signal).then(result => {
          if (closed || suspended || generation !== currentGeneration) return;
          probe = undefined;
          if (result === "ended") { window.dispatchEvent(new globalThis.Event("connect-ended")); return; }
          resume();
        });
      }, delay);
      delay = Math.min(delay * 2, 10_000);
    }
    cancel = () => { active = false; cancelWatchdog(); };
    current.addEventListener("hello", message => {
      if (!alive()) return;
      // Each connection may have missed events the server does not replay, such as read positions.
      const value: unknown = JSON.parse(message.data);
      if (typeof value === "object" && value !== null && "build" in value && typeof value.build === "string") {
        // An idle stream may send only hello and heartbeat comments. Treat hello
        // as recovery too, so the next outage does not inherit this one's backoff.
        delay = 500;
        const hello: StreamHello = { build: value.build };
        if ("roomUnion" in value && typeof value.roomUnion === "boolean") hello.roomUnion = value.roomUnion;
        if ("heartbeatMs" in value && typeof value.heartbeatMs === "number" && Number.isFinite(value.heartbeatMs) && value.heartbeatMs > 0 && value.heartbeatMs <= 60_000) {
          hello.heartbeatMs = value.heartbeatMs;
          current.addEventListener("heartbeat", () => { if (alive()) refresh(); });
        }
        heartbeatMs = hello.heartbeatMs;
        refresh();
        opened(reconnect, hello);
        if (!alive()) return;
        onHello(hello);
        if (owner) { reportStreamState(true); for (const listener of transcriptListeners) listener(); }
      }
    });
    current.onmessage = message => {
      if (!alive()) return;
      const event = decodeEvent(JSON.parse(message.data));
      if (event) {
        refresh();
        // Turn boundaries need not append transcript bytes: reload at start so a cold
        // session shows Working before Pi writes, and at end so live steps settle.
        if ((owner && event.type === "transcript.changed") || (room && (!attentionAfter || event.room === room) && (event.type === "turn.started" || event.type === "turn.ended"))) for (const listener of transcriptListeners) listener(event.body.session);
        apply(event);
      }
      delay = 500;
    };
    current.onerror = failed;
  }
  // BFCache preserves the component, so its disposer does not run on pagehide.
  // Release HTTP/1 connections while away and resume from the latest applied cursor.
  function hide() {
    if (closed || suspended) return;
    suspended = true;
    generation++;
    probe?.abort();
    cancel();
    source.close();
  }
  function show() {
    if (closed || !suspended) return;
    suspended = false;
    connect();
  }
  lifecycle?.addEventListener("pagehide", hide);
  lifecycle?.addEventListener("pageshow", show);
  connect();
  return () => {
    closed = true;
    probe?.abort();
    cancel();
    source.close();
    lifecycle?.removeEventListener("pagehide", hide);
    lifecycle?.removeEventListener("pageshow", show);
  };
}
