import test from "node:test";
import assert from "node:assert/strict";
import { Alt, Ctrl, PhoneKeys, Shift, Finger, escTwice, keyboardInput, screenPoint, textInputs, type Modifiers } from "./computerInput.ts";

const none: Modifiers = { ctrl: false, alt: false };

test("typed text, Enter, Tab and non-Latin text become keys and one insert", () => {
  assert.deepEqual(textInputs("Hi\n\t日本😀", none), [
    { type: "key", key: "H", code: "KeyH", vk: 72, modifiers: 0, text: "H" },
    { type: "key", key: "i", code: "KeyI", vk: 73, modifiers: 0, text: "i" },
    { type: "key", key: "Enter", code: "Enter", vk: 13, modifiers: 0, text: "\r" },
    { type: "key", key: "Tab", code: "Tab", vk: 9, modifiers: 0 },
    { type: "text", text: "日本😀" },
  ]);
});

test("Ctrl and Alt apply to one key, then release before the next", () => {
  const seen: Modifiers[] = [];
  const keys = new PhoneKeys(state => seen.push(state));
  keys.press("ctrl");
  keys.press("alt");
  assert.deepEqual(keys.text("lx"), [
    { type: "key", key: "l", code: "KeyL", vk: 76, modifiers: Ctrl | Alt },
    { type: "key", key: "x", code: "KeyX", vk: 88, modifiers: 0, text: "x" },
  ]);
  assert.deepEqual(keys.press("tab"), [{ type: "key", key: "Tab", code: "Tab", vk: 9, modifiers: 0 }]);
  assert.deepEqual(seen.at(-1), none);
  keys.press("ctrl");
  assert.deepEqual(keys.erase(true), [{ type: "key", key: "Delete", code: "Delete", vk: 46, modifiers: Ctrl }]);
});

test("a hardware key keeps its text, becomes a shortcut with Ctrl, and leaves the system key out", () => {
  const press = (key: string, extra: Partial<Parameters<typeof keyboardInput>[0]> = {}) =>
    keyboardInput({ key, code: key.length === 1 ? `Key${key.toUpperCase()}` : key, keyCode: key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0, ctrlKey: false, altKey: false, shiftKey: false, metaKey: false, isComposing: false, ...extra });
  assert.deepEqual(press("A", { shiftKey: true }), { type: "key", key: "A", code: "KeyA", vk: 65, modifiers: Shift, text: "A" });
  assert.deepEqual(press("c", { ctrlKey: true }), { type: "key", key: "c", code: "KeyC", vk: 67, modifiers: Ctrl });
  assert.equal(press("c", { metaKey: true }), undefined);
  assert.equal(press("Shift"), undefined);
  assert.equal(press("é", { isComposing: true }), undefined);
  assert.equal(press("F5")?.vk, 116);
  assert.equal(press("ArrowDown")?.vk, 40);
});

test("a pointer lands in the picture, not on the bars around it", () => {
  const box = { left: 10, top: 20, width: 400, height: 400 };
  const picture = { width: 200, height: 100 };
  assert.deepEqual(screenPoint(box, picture, 210, 220), { x: 100, y: 50 });
  assert.equal(screenPoint(box, picture, 210, 100), undefined);
  assert.equal(screenPoint({ ...box, width: 0 }, picture, 210, 220), undefined);
});

test("a short touch clicks, and a drag scrolls without clicking", () => {
  const touch = new Finger();
  touch.down({ x: 100, y: 100 }, { x: 10, y: 10 });
  assert.deepEqual(touch.move({ x: 103, y: 102 }, { x: 11, y: 11 }), []);
  assert.deepEqual(touch.up(), [
    { type: "move", x: 10, y: 10 }, { type: "down", x: 10, y: 10, button: "left" }, { type: "up", x: 10, y: 10, button: "left" },
  ]);
  touch.down({ x: 100, y: 100 }, { x: 10, y: 10 });
  assert.deepEqual(touch.move({ x: 100, y: 80 }, { x: 10, y: 8 }), [{ type: "wheel", x: 10, y: 8, dx: 0, dy: 20 }]);
  assert.deepEqual(touch.move({ x: 100, y: 70 }, { x: 10, y: 7 }), [{ type: "wheel", x: 10, y: 7, dx: 0, dy: 10 }]);
  assert.deepEqual(touch.up(), []);
});

test("Esc twice within the gap returns control, and a third press starts over", () => {
  const twice = escTwice(600);
  assert.deepEqual([twice(0), twice(500), twice(600), twice(1300)], [false, true, false, false]);
});
