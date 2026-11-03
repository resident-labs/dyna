import test from "node:test";
import assert from "node:assert/strict";
import { PhoneKeys, doubleTap, escTwice, keysymOf, textInputs, type KeyEvent, type Modifiers } from "./computerInput.ts";

const none: Modifiers = { ctrl: false, alt: false };
const tap = (keysym: number): KeyEvent[] => [{ keysym, down: true }, { keysym, down: false }];
const Control = 0xffe3, Alt = 0xffe9;

test("typed text, Enter, Tab and non-Latin text become keysym presses", () => {
  assert.deepEqual(textInputs("Hi\n\té日😀", none), [
    ...tap(0x48), ...tap(0x69), ...tap(0xff0d), ...tap(0xff09), ...tap(0xe9), ...tap(0x01000000 + 0x65e5), ...tap(0x01000000 + 0x1f600),
  ]);
  assert.equal(keysymOf(" "), 0x20);
});

test("Ctrl and Alt apply to one key, around it, then release before the next", () => {
  const seen: Modifiers[] = [];
  const keys = new PhoneKeys(state => seen.push(state));
  keys.press("ctrl");
  keys.press("alt");
  assert.deepEqual(keys.text("lx"), [
    { keysym: Control, down: true }, { keysym: Alt, down: true }, ...tap(0x6c), { keysym: Alt, down: false }, { keysym: Control, down: false }, ...tap(0x78),
  ]);
  assert.deepEqual(keys.press("tab"), tap(0xff09));
  assert.deepEqual(seen.at(-1), none);
  keys.press("ctrl");
  assert.deepEqual(keys.erase(true), [{ keysym: Control, down: true }, ...tap(0xffff), { keysym: Control, down: false }]);
  assert.deepEqual(keys.named("Dead"), []);
});

test("Esc twice within the gap leaves full screen, and a third press starts over", () => {
  const twice = escTwice(600);
  assert.deepEqual([twice(0), twice(500), twice(700), twice(1400)], [false, true, false, false]);
});

test("a second press near the first and soon after is a double tap", () => {
  const tapped = doubleTap(350, 24);
  assert.deepEqual([tapped(0, 10, 10), tapped(300, 20, 20), tapped(400, 20, 20), tapped(1000, 20, 20), tapped(1100, 90, 20)], [false, true, false, false, false]);
});
