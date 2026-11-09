import type { Event, StreamHello } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export interface EventConnection {
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  addEventListener(type: "hello" | "heartbeat", listener: (event: MessageEvent<string>) => void): void;
  close(): void;
}
export type EventFactory = (url: string) => EventConnection;
export type Schedule = (callback: () => void, delay: number) => () => void;

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
  factory: EventFactory = url => new EventSource(url),
  schedule: Schedule = (callback, delay) => {
    const id = setTimeout(callback, delay);
    return () => clearTimeout(id);
  },
  opened: (reconnect?: boolean) => void = () => {},
  lifecycle: Pick<EventTarget, "addEventListener" | "removeEventListener"> | undefined = typeof window === "undefined" ? undefined : window,
): () => void {
  let source: EventConnection;
  let cancel = () => {};
  let closed = false;
  let delay = 500;
  let attempted = false;
  let suspended = false;
  let generation = 0;
  function connect() {
    const currentGeneration = ++generation;
    const reconnect = attempted;
    attempted = true;
    const current = source = factory(`/v1/events?${new URLSearchParams({ ...(room && { room }), after: String(after()) })}`);
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
      if (!room) reportStreamState(false);
      cancel = schedule(() => { if (!closed && !suspended && generation === currentGeneration) connect(); }, delay);
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
        if ("heartbeatMs" in value && typeof value.heartbeatMs === "number" && Number.isFinite(value.heartbeatMs) && value.heartbeatMs > 0 && value.heartbeatMs <= 60_000) {
          hello.heartbeatMs = value.heartbeatMs;
          current.addEventListener("heartbeat", () => { if (alive()) refresh(); });
        }
        heartbeatMs = hello.heartbeatMs;
        refresh();
        opened(reconnect);
        onHello(hello);
        if (!room) { reportStreamState(true); for (const listener of transcriptListeners) listener(); }
      }
    });
    current.onmessage = message => {
      if (!alive()) return;
      const event = decodeEvent(JSON.parse(message.data));
      if (event) {
        refresh();
        // Turn boundaries need not append transcript bytes: reload at start so a cold
        // session shows Working before Pi writes, and at end so live steps settle.
        if (!room && event.type === "transcript.changed" || room && (event.type === "turn.started" || event.type === "turn.ended")) for (const listener of transcriptListeners) listener(event.body.session);
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
    cancel();
    source.close();
    lifecycle?.removeEventListener("pagehide", hide);
    lifecycle?.removeEventListener("pageshow", show);
  };
}
