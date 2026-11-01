import { test } from "node:test";
import assert from "node:assert/strict";
import { ComposerDrafts } from "./composer-drafts.ts";

class MemoryStorage {
  private items = new Map<string, string>();
  getItem(key: string): string | null { return this.items.get(key) ?? null; }
  setItem(key: string, value: string): void { this.items.set(key, value); }
  removeItem(key: string): void { this.items.delete(key); }
}

// The test fires the pending timer by hand, so no real time passes.
function manualTimers() {
  let pending: (() => void) | undefined;
  return {
    timers: {
      setTimeout: (run: () => void) => { pending = run; return 1; },
      clearTimeout: () => { pending = undefined; },
    },
    fire: () => { const run = pending; pending = undefined; run?.(); },
  };
}

test("a typed draft writes its latest text once, after typing pauses, and survives a reload", () => {
  const storage = new MemoryStorage();
  const { timers, fire } = manualTimers();
  const drafts = new ComposerDrafts(storage, timers);
  drafts.save("room/", "first");
  drafts.save("room/", "second");
  assert.equal(storage.getItem("dyna.draft:room/"), null);
  fire();
  assert.equal(new ComposerDrafts(storage).load("room/"), "second");
});

test("sending a draft clears it and drops its pending write", () => {
  const storage = new MemoryStorage();
  const { timers, fire } = manualTimers();
  const drafts = new ComposerDrafts(storage, timers);
  drafts.save("room/session", "hello");
  drafts.clear("room/session");
  fire();
  assert.equal(new ComposerDrafts(storage).load("room/session"), "");
});
