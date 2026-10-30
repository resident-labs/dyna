import type { Event } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export interface EventConnection {
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: globalThis.Event) => void) | null;
  close(): void;
}
export type EventFactory = (url: string) => EventConnection;
export type Schedule = (callback: () => void, delay: number) => () => void;

export function stream(
  room: string, after: () => number, apply: (event: Event) => void,
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
    source = factory(`/v1/events?${new URLSearchParams({ room, after: String(after()) })}`);
    source.onmessage = message => {
      const event = decodeEvent(message.data);
      apply(event);
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
