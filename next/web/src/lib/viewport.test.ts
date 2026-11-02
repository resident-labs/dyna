import assert from "node:assert/strict";
import { test } from "node:test";
import { canScroll, panned, zoomed } from "./viewport.ts";

type Fake = { parentElement: Fake | null; scrollHeight: number; clientHeight: number; scrollWidth: number; clientWidth: number; overflowX: string; overflowY: string };
const box = (parentElement: Fake | null, fields: Partial<Fake> = {}): Fake =>
  ({ parentElement, scrollHeight: 100, clientHeight: 100, scrollWidth: 100, clientWidth: 100, overflowX: "visible", overflowY: "visible", ...fields });
const scrolls = (from: Fake | null, root: Fake) => canScroll(from, root, item => item);

test("a drag on a box that fits has nothing to scroll", () => {
  const root = box(null), field = box(root, { overflowY: "auto" });
  assert.equal(scrolls(field, root), false);
});

test("a box scrolls only when its content overflows the axis it may scroll", () => {
  const root = box(null);
  assert.equal(scrolls(box(root, { overflowY: "auto", scrollHeight: 300 }), root), true);
  assert.equal(scrolls(box(root, { overflowY: "scroll", scrollHeight: 300 }), root), true);
  assert.equal(scrolls(box(root, { overflowX: "auto", scrollWidth: 300 }), root), true);
  assert.equal(scrolls(box(root, { overflowY: "hidden", scrollHeight: 300 }), root), false);
  assert.equal(scrolls(box(root, { overflowY: "visible", scrollHeight: 300 }), root), false);
  assert.equal(scrolls(box(root, { overflowY: "auto", scrollWidth: 300 }), root), false);
});

test("a child of a scrolling box can scroll that box", () => {
  const root = box(null), tray = box(root, { overflowX: "auto", scrollWidth: 300 }), chip = box(tray);
  assert.equal(scrolls(chip, root), true);
});

test("the walk stops at root, so the panes around it do not count", () => {
  const feed = box(null, { overflowY: "auto", scrollHeight: 900 }), composer = box(feed), field = box(composer);
  assert.equal(scrolls(field, composer), false);
});

test("a touch with no box scrolls nothing", () => {
  assert.equal(scrolls(null, box(null)), false);
});

test("a page that moved at normal scale counts as panned", () => {
  assert.equal(panned({ offsetTop: 120, scale: 1 }, 0), true);
  assert.equal(panned({ offsetTop: 0, scale: 1 }, 40), true);
  assert.equal(panned(undefined, 40), true);
});

test("a page in place is not panned, nor is a rubber band past the top", () => {
  assert.equal(panned({ offsetTop: 0, scale: 1 }, 0), false);
  assert.equal(panned({ offsetTop: -12, scale: 1 }, -12), false);
  assert.equal(panned(undefined, 0), false);
  assert.equal(panned(null, 0), false);
});

test("a reader panning while zoomed is left alone", () => {
  assert.equal(panned({ offsetTop: 120, scale: 2 }, 80), false);
  assert.equal(zoomed(2), true);
  assert.equal(zoomed(1.005), false);
  assert.equal(zoomed(undefined), false);
});
