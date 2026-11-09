import { test } from "node:test";
import assert from "node:assert/strict";
import { stream, type EventConnection } from "./stream.ts";

for (const room of [undefined, "room"]) {
  for (const retryPending of [false, true]) {
    test(`page lifecycle releases and resumes ${room ?? "owner"} stream, retry pending=${retryPending}`, () => {
      class Lifecycle extends EventTarget {
        listeners = new Set<EventListenerOrEventListenerObject>();
        override addEventListener(type: string, callback: EventListenerOrEventListenerObject | null) {
          if (callback) this.listeners.add(callback);
          super.addEventListener(type, callback);
        }
        override removeEventListener(type: string, callback: EventListenerOrEventListenerObject | null) {
          if (callback) this.listeners.delete(callback);
          super.removeEventListener(type, callback);
        }
      }
      const lifecycle = new Lifecycle();
      const tasks = new Set<() => void>();
      const scheduled: (() => void)[] = [];
      const sources: { source: EventConnection; closed: boolean; listeners: Map<string, (event: MessageEvent<string>) => void> }[] = [];
      const urls: string[] = [];
      const opens: (boolean | undefined)[] = [];
      let seq = 3;
      const stop = stream(room, () => seq, event => { seq = event.seq; }, url => {
        urls.push(url);
        const entry = { source: {} as EventConnection, closed: false, listeners: new Map<string, (event: MessageEvent<string>) => void>() };
        entry.source = {
          onmessage: null, onerror: null,
          addEventListener(type, listener) { entry.listeners.set(type, listener); },
          close() { entry.closed = true; },
        };
        sources.push(entry);
        return entry.source;
      }, callback => {
        tasks.add(callback);
        scheduled.push(callback);
        return () => { tasks.delete(callback); };
      }, reconnect => opens.push(reconnect), lifecycle);
      const emit = (type: string) => lifecycle.dispatchEvent(new Event(type));
      const hello = (index: number) => sources[index].listeners.get("hello")!(new MessageEvent("hello", { data: '{"build":"b","heartbeatMs":15000}' }));
      // The initial pageshow belongs to the already-open document.
      emit("pageshow"); emit("pageshow");
      assert.equal(sources.length, 1);
      hello(0);
      assert.deepEqual(opens, [false]);
      assert.equal(tasks.size, 1);
      if (retryPending) sources[0].source.onerror?.(new Event("error"));
      assert.equal(tasks.size, 1);
      emit("pagehide"); emit("pagehide");
      assert.equal(sources[0].closed, true);
      assert.equal(tasks.size, 0);
      // Even already-queued callbacks cannot restart or mutate the suspended view.
      for (const callback of [...scheduled]) callback();
      hello(0);
      sources[0].source.onmessage?.(new MessageEvent("message", { data: JSON.stringify({
        seq: 99, id: "e", at: "", from: { kind: "owner" }, type: "session.closed", body: { session: "s" },
      }) }));
      sources[0].source.onerror?.(new Event("error"));
      assert.equal(seq, 3);
      assert.equal(sources.length, 1);
      assert.equal(tasks.size, 0);
      seq = 7;
      emit("pageshow"); emit("pageshow");
      assert.equal(sources.length, 2);
      assert.equal(new URL(urls[1], "http://localhost").searchParams.get("after"), "7");
      for (const callback of [...scheduled]) callback();
      hello(0);
      assert.equal(sources.length, 2);
      hello(1);
      assert.deepEqual(opens, [false, true]);
      assert.equal(tasks.size, 1);
      emit("pagehide"); emit("pageshow");
      assert.equal(sources.length, 3);
      assert.equal(sources[1].closed, true);
      // Disposal while suspended must remove listeners and forbid future reopen.
      emit("pagehide");
      stop();
      assert.equal(lifecycle.listeners.size, 0);
      emit("pageshow"); emit("pagehide");
      for (const callback of scheduled) callback();
      assert.equal(sources.length, 3);
      assert.equal(tasks.size, 0);
    });
  }
}
