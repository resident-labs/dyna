import test from "node:test";
import assert from "node:assert/strict";
import { Alt, Ctrl, Finger, PhoneKeys, Shift, escTwice, keyboardInput, pictureScale, screenPoint, textInputs, type ComputerInput, type Modifiers } from "./computerInput.ts";

const none: Modifiers = { ctrl: false, alt: false };
const tap = (key: string, code: string, modifiers = 0): ComputerInput[] => [
  { kind: "key.down", key, code, modifiers, x: 0, y: 0 }, { kind: "key.up", key, code, modifiers, x: 0, y: 0 },
];

test("typed text, Enter, Tab and non-Latin text become key presses and one char insert", () => {
  assert.deepEqual(textInputs("Hi\n\t日本😀", none), [
    ...tap("H", "KeyH"), ...tap("i", "KeyI"), ...tap("Enter", "Enter"), ...tap("Tab", "Tab"), { kind: "char", text: "日本😀", x: 0, y: 0 },
  ]);
});

test("Ctrl and Alt apply to one key, then release before the next", () => {
  const seen: Modifiers[] = [];
  const keys = new PhoneKeys(state => seen.push(state));
  keys.press("ctrl");
  keys.press("alt");
  assert.deepEqual(keys.text("lx"), [...tap("l", "KeyL", Ctrl | Alt), ...tap("x", "KeyX")]);
  assert.deepEqual(keys.press("tab"), tap("Tab", "Tab"));
  assert.deepEqual(seen.at(-1), none);
  keys.press("ctrl");
  assert.deepEqual(keys.erase(true), tap("Delete", "Delete", Ctrl));
});

test("a hardware key sends its down and up, and the system key and bare modifiers send nothing", () => {
  const press = (key: string, extra: Partial<Parameters<typeof keyboardInput>[0]> = {}, down = true) =>
    keyboardInput({ key, code: key.length === 1 ? `Key${key.toUpperCase()}` : key, keyCode: 0, ctrlKey: false, altKey: false, shiftKey: false, metaKey: false, isComposing: false, ...extra }, down);
  assert.deepEqual(press("A", { shiftKey: true }), [{ kind: "key.down", key: "A", code: "KeyA", modifiers: Shift, x: 0, y: 0 }]);
  assert.deepEqual(press("c", { ctrlKey: true }, false), [{ kind: "key.up", key: "c", code: "KeyC", modifiers: Ctrl, x: 0, y: 0 }]);
  assert.deepEqual(press("F5").map(event => event.key), ["F5"]);
  for (const none of [press("c", { metaKey: true }), press("Shift"), press("é", { isComposing: true }), press("Dead")]) assert.deepEqual(none, []);
});

test("a pointer lands in the picture, not on the bars around it", () => {
  const box = { left: 10, top: 20, width: 400, height: 400 };
  const picture = { width: 200, height: 100 };
  assert.deepEqual(screenPoint(box, picture, 210, 220), { x: 100, y: 50 });
  assert.equal(screenPoint(box, picture, 210, 100), undefined);
  assert.equal(screenPoint({ ...box, width: 0 }, picture, 210, 220), undefined);
  assert.equal(pictureScale(box, picture), 2);
});

test("a short touch clicks, and a drag scrolls in picture pixels without clicking", () => {
  const finger = new Finger();
  finger.down({ x: 100, y: 100 }, { x: 10, y: 10 });
  assert.deepEqual(finger.move({ x: 103, y: 102 }, { x: 11, y: 11 }, 2), []);
  assert.deepEqual(finger.up(), [
    { kind: "mouse.move", x: 10, y: 10 }, { kind: "mouse.down", x: 10, y: 10, button: "left" }, { kind: "mouse.up", x: 10, y: 10, button: "left" },
  ]);
  finger.down({ x: 100, y: 100 }, { x: 10, y: 10 });
  assert.deepEqual(finger.move({ x: 100, y: 80 }, { x: 10, y: 8 }, 2), [{ kind: "wheel", x: 10, y: 8, deltaX: 0, deltaY: 10 }]);
  assert.deepEqual(finger.move({ x: 100, y: 70 }, { x: 10, y: 7 }, 2), [{ kind: "wheel", x: 10, y: 7, deltaX: 0, deltaY: 5 }]);
  assert.deepEqual(finger.up(), []);
});

test("Esc twice within the gap returns control, and a third press starts over", () => {
  const twice = escTwice(600);
  assert.deepEqual([twice(0), twice(500), twice(600), twice(1300)], [false, true, false, false]);
});
