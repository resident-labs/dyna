import type { Event, StreamHello } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export interface EventConnection {
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  addEventListener(type: "hello", listener: (event: MessageEvent<string>) => void): void;
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
  opened: () => void = () => {},
): () => void {
  let source: EventConnection;
  let cancel = () => {};
  let closed = false;
  let delay = 500;
  function connect() {
    source = factory(`/v1/events?${new URLSearchParams({ ...(room && { room }), after: String(after()) })}`);
    source.addEventListener("hello", message => {
      // Each connection may have missed events the server does not replay, such as read positions.
      opened();
      const value: unknown = JSON.parse(message.data);
      if (typeof value === "object" && value !== null && "build" in value && typeof value.build === "string") {
        const hello: StreamHello = { build: value.build };
        onHello(hello);
        if (!room) { reportStreamState(true); for (const listener of transcriptListeners) listener(); }
      }
    });
    source.onmessage = message => {
      const event = decodeEvent(JSON.parse(message.data));
      if (event) {
        if (!room && event.type === "transcript.changed") for (const listener of transcriptListeners) listener(event.body.session);
        apply(event);
      }
      delay = 500;
    };
    source.onerror = () => {
      source.close();
      if (closed) return;
      if (!room) reportStreamState(false);
      cancel = schedule(connect, delay);
      delay = Math.min(delay * 2, 10_000);
    };
  }
  connect();
  return () => { closed = true; cancel(); source.close(); };
}
