import type { Event } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export interface EventConnection {
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  onopen: ((event: globalThis.Event) => void) | null;
  close(): void;
}
export type EventFactory = (url: string) => EventConnection;
export type Schedule = (callback: () => void, delay: number) => () => void;

// Without a room, the owner stream carries the attention events of every room. opened runs on each
// connect, so a caller can reload what the stream does not replay.
export function stream(
  room: string | undefined, after: () => number, apply: (event: Event) => void, opened: () => void = () => {},
  factory: EventFactory = url => new EventSource(url),
  schedule: Schedule = (callback, delay) => {
    const id = setTimeout(callback, delay);
    return () => clearTimeout(id);
  },
): () => void {
  let source: EventConnection;
  let cancel = () => {};
  let closed = false;
  let delay = 500;
  function connect() {
    source = factory(`/v1/events?${new URLSearchParams({ ...(room && { room }), after: String(after()) })}`);
    source.onopen = () => opened();
    source.onmessage = message => {
      const event = decodeEvent(JSON.parse(message.data));
      if (event) apply(event);
      delay = 500;
    };
    source.onerror = () => {
      source.close();
      if (closed) return;
      cancel = schedule(connect, delay);
      delay = Math.min(delay * 2, 10_000);
    };
  }
  connect();
  return () => { closed = true; cancel(); source.close(); };
}
