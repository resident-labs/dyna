import { test } from "node:test";
import assert from "node:assert/strict";
import { layoutRows, listKey, listView, visibleRange, type Chip, type Column, type Row } from "./settings-list.ts";

type Room = { id: string; name: string; on: number; group: string };
const text = (room: Room) => room.name;
const key = (room: Room) => room.id;
// A thousand rooms as a heavy owner might have: names repeat a few words, and a fifth of them have every switch on.
const many = Array.from({ length: 1000 }, (_, i): Room => ({ id: `room_${i}`, name: `${["web", "api", "docs", "billing"][i % 4]}-${i}`, on: i % 5 === 0 ? 4 : i % 3, group: i % 2 ? "odd" : "even" }));
const chips: Chip<Room>[] = [{ id: "all", label: "All" }, { id: "auto", label: "Autopilot", test: room => room.on === 4 }, { id: "off", label: "All off", test: room => room.on === 0 }];
const names = (rows: Row<Room>[]) => rows.map(row => row.kind === "item" ? row.item.name : `[${row.group.title}]`);

test("a search keeps the items whose text holds every word, in any case", () => {
  const view = listView(many, { text, key, query: "  WEB 10 ", chip: chips[0], folded: new Set() });
  assert.ok(view.rows.length > 0);
  for (const row of view.rows) assert.ok(row.kind === "item" && row.item.name.startsWith("web-") && row.item.name.includes("10"));
  assert.equal(view.shown, view.rows.length);
  assert.equal(listView(many, { text, key, query: "zzzz", chip: chips[0], folded: new Set() }).rows.length, 0);
});

test("a chip filters, and every chip's count is over the search's matches", () => {
  const view = listView(many, { text, key, query: "docs", chip: chips[1], chips, folded: new Set() });
  const docs = many.filter(room => room.name.startsWith("docs"));
  assert.deepEqual(view.counts, { all: docs.length, auto: docs.filter(room => room.on === 4).length, off: docs.filter(room => room.on === 0).length });
  assert.equal(view.shown, docs.filter(room => room.on === 4).length);
});

test("a column sorts both ways and leaves the items' own order alone", () => {
  const rooms = [many[2], many[0], many[1]];
  const name: Column<Room> = { id: "name", label: "Room", width: "1fr", compare: (a, b) => a.name.localeCompare(b.name) };
  assert.deepEqual(names(listView(rooms, { text, key, query: "", chip: chips[0], sort: { column: name, dir: 1 }, folded: new Set() }).rows), ["api-1", "docs-2", "web-0"]);
  assert.deepEqual(names(listView(rooms, { text, key, query: "", chip: chips[0], sort: { column: name, dir: -1 }, folded: new Set() }).rows), ["web-0", "docs-2", "api-1"]);
  assert.deepEqual(names(listView(rooms, { text, key, query: "", chip: chips[0], folded: new Set() }).rows), ["docs-2", "web-0", "api-1"]);
  assert.deepEqual(rooms.map(room => room.name), ["docs-2", "web-0", "api-1"]);
});

test("groups come in the order of their first item, and a folded group keeps its head and count", () => {
  const group = (room: Room) => ({ id: room.group, title: room.group });
  const rooms = many.slice(0, 4);
  assert.deepEqual(names(listView(rooms, { text, key, query: "", chip: chips[0], group, folded: new Set() }).rows), ["[even]", "web-0", "docs-2", "[odd]", "api-1", "billing-3"]);
  const folded = listView(rooms, { text, key, query: "", chip: chips[0], group, folded: new Set(["even"]) });
  assert.deepEqual(names(folded.rows), ["[even]", "[odd]", "api-1", "billing-3"]);
  assert.deepEqual(folded.rows[0], { kind: "group", group: { id: "even", title: "even" }, count: 2, folded: true });
  assert.equal(folded.shown, 4);
  // A search shows what it found, so it opens every group.
  assert.deepEqual(names(listView(rooms, { text, key, query: "web", chip: chips[0], group, folded: new Set(["even"]) }).rows), ["[even]", "web-0"]);
});

test("rows lie one under the next, a group head shorter than an item", () => {
  const rows: Row<Room>[] = [{ kind: "group", group: { id: "g", title: "g" }, count: 2, folded: false }, { kind: "item", key: "a", item: many[0] }, { kind: "item", key: "b", item: many[1] }];
  assert.deepEqual(layoutRows(rows, 52, 34), { tops: [0, 34, 86], total: 138 });
  assert.deepEqual(layoutRows([], 52, 34), { tops: [], total: 0 });
});

test("a thousand rows draw about twenty: the ones in view and a margin each way", () => {
  const { rows } = listView(many, { text, key, query: "", chip: chips[0], folded: new Set() });
  const { tops } = layoutRows(rows, 52, 34);
  const top = visibleRange(tops, 0, 600, 200);
  assert.equal(top.start, 0);
  assert.ok(top.end - top.start <= 20, `${top.end - top.start} rows`);
  const middle = visibleRange(tops, 52 * 500, 600, 200);
  assert.ok(middle.start <= 500 - 3 && middle.end >= 500 + 12, JSON.stringify(middle));
  assert.ok(middle.end - middle.start <= 24, `${middle.end - middle.start} rows`);
  const end = visibleRange(tops, 52 * 1000 - 600, 600, 200);
  assert.equal(end.end, 1000);
  assert.deepEqual(visibleRange([], 0, 600, 200), { start: 0, end: 0 });
});

test("the keys move within the list, open an item and fold a group", () => {
  const rows: Row<Room>[] = [{ kind: "group", group: { id: "g", title: "g" }, count: 3, folded: false }, ...many.slice(0, 3).map(room => ({ kind: "item" as const, key: room.id, item: room }))];
  assert.deepEqual(listKey("ArrowDown", -1, rows, 10), { move: 0 });
  assert.deepEqual(listKey("ArrowDown", 1, rows, 10), { move: 2 });
  assert.deepEqual(listKey("ArrowDown", 3, rows, 10), { move: 3 });
  assert.deepEqual(listKey("ArrowUp", 0, rows, 10), { move: 0 });
  assert.deepEqual(listKey("PageDown", 0, rows, 10), { move: 3 });
  assert.deepEqual(listKey("PageUp", 3, rows, 2), { move: 1 });
  assert.deepEqual(listKey("Home", 2, rows, 10), { move: 0 });
  assert.deepEqual(listKey("End", 0, rows, 10), { move: 3 });
  assert.deepEqual(listKey("Enter", 2, rows, 10), { open: 2 });
  assert.deepEqual(listKey("Enter", 0, rows, 10), { fold: 0 });
  assert.deepEqual(listKey(" ", 0, rows, 10), { fold: 0 });
  // Left folds an open group, right opens a folded one; neither does anything on an item.
  assert.deepEqual(listKey("ArrowLeft", 0, rows, 10), { fold: 0 });
  assert.equal(listKey("ArrowRight", 0, rows, 10), undefined);
  assert.equal(listKey("ArrowLeft", 1, rows, 10), undefined);
  const shut: Row<Room>[] = [{ kind: "group", group: { id: "g", title: "g" }, count: 3, folded: true }];
  assert.deepEqual(listKey("ArrowRight", 0, shut, 10), { fold: 0 });
  assert.equal(listKey("Enter", -1, rows, 10), undefined);
  assert.equal(listKey("ArrowDown", -1, [], 10), undefined);
  assert.equal(listKey("1", 1, rows, 10), undefined);
});
