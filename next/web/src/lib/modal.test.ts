import { test } from "node:test";
import assert from "node:assert/strict";
import { Confirmations, onBackdrop, returnFocus } from "./modal.ts";

test("only a press that starts and ends on the backdrop closes the modal", () => {
  const dialog = {}, inside = {};
  assert.equal(onBackdrop(dialog, dialog, dialog), true);
  assert.equal(onBackdrop(dialog, inside, dialog), false, "a drag from the panel out to the backdrop");
  assert.equal(onBackdrop(dialog, dialog, inside), false);
  assert.equal(onBackdrop(dialog, null, dialog), false);
});

test("focus goes back to the first opener still on the page", () => {
  const focused: string[] = [];
  const element = (name: string, isConnected: boolean) => ({ isConnected, focus: () => { focused.push(name); } });
  const menuItem = element("menu item", false), trigger = element("trigger", true), row = element("row", true);
  assert.equal(returnFocus(menuItem, trigger, row), trigger);
  assert.equal(returnFocus(undefined, row), row);
  assert.equal(returnFocus(null, menuItem), undefined);
  assert.deepEqual(focused, ["trigger", "row"]);
});

test("a confirm the owner leaves answers false, and one without work answers true", async () => {
  const confirms = new Confirmations();
  const left = confirms.ask({ title: "Close a?", action: "Close" });
  assert.equal(confirms.current?.title, "Close a?");
  assert.equal(confirms.cancel(), true);
  assert.equal(await left, false);
  assert.equal(confirms.current, undefined);
  const taken = confirms.ask({ title: "Go?", action: "Go" });
  await confirms.accept();
  assert.equal(await taken, true);
  assert.equal(confirms.current, undefined);
});

test("a confirm stays open and busy while its work runs, and nothing closes it", async () => {
  const confirms = new Confirmations();
  let finish!: () => void;
  const answer = confirms.ask({ title: "Close a?", action: "Close", run: () => new Promise<void>(resolve => { finish = resolve; }) });
  const running = confirms.accept();
  assert.equal(confirms.current?.busy, true);
  assert.equal(confirms.cancel(), false);
  await confirms.accept();
  finish();
  await running;
  assert.equal(await answer, true);
  assert.equal(confirms.current, undefined);
});

test("a failed run keeps the confirm open with the reason, and a retry clears it", async () => {
  const confirms = new Confirmations();
  let tries = 0;
  const answer = confirms.ask({ title: "Close a?", action: "Close", failed: error => `No: ${String(error)}`, run: async () => { if (tries++ === 0) throw "busy"; } });
  await confirms.accept();
  assert.deepEqual([confirms.current?.busy, confirms.current?.problem], [false, "No: busy"]);
  await confirms.accept();
  assert.equal(await answer, true);
  const fallback = confirms.ask({ title: "Go?", action: "Go", run: () => Promise.reject(new Error("x")) });
  await confirms.accept();
  assert.equal(confirms.current?.problem, "That did not work. Try again.");
  confirms.cancel();
  assert.equal(await fallback, false);
});

test("asking again replaces the confirm on screen, and the first answers false", async () => {
  const confirms = new Confirmations();
  const seen: (string | undefined)[] = [];
  const stop = confirms.subscribe(() => seen.push(confirms.current?.title));
  const first = confirms.ask({ title: "A?", action: "A" });
  const second = confirms.ask({ title: "B?", action: "B" });
  assert.equal(await first, false);
  assert.equal(confirms.current?.title, "B?");
  confirms.cancel();
  assert.equal(await second, false);
  stop();
  confirms.ask({ title: "C?", action: "C" });
  assert.deepEqual(seen, [undefined, "A?", undefined, "B?", undefined]);
});

test("work that settles after its confirm was replaced changes nothing", async () => {
  const confirms = new Confirmations();
  let fail!: (error: unknown) => void;
  void confirms.ask({ title: "A?", action: "A", run: () => new Promise((_, reject) => { fail = reject; }) });
  const running = confirms.accept();
  void confirms.ask({ title: "B?", action: "B" });
  fail("late");
  await running;
  assert.deepEqual([confirms.current?.title, confirms.current?.problem], ["B?", ""]);
});
