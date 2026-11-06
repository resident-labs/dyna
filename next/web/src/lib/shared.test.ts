import test from "node:test";
import assert from "node:assert/strict";
import { sharedFileSize, sharedMedia, sharedPaths, sharedURL } from "./shared.ts";
import { roomRoute } from "./routes.ts";
import { panelTab, type PanelTab } from "./panel-tabs.ts";
import { sharedRows } from "./shared-tree.ts";
import { mediaKind } from "./media.ts";

test("shared paths trim punctuation and closing quotes and brackets, ignoring private paths", () => {
  assert.deepEqual(sharedPaths('Read /box/shared/design/note.md.,:; and `/box/shared/a.go` or "/box/shared/b.png") /box/work/private /box/home/private').map(match => match.path), ["design/note.md", "a.go", "b.png"]);
  assert.equal(sharedPaths("/box/shared/").length, 0);
  assert.equal(sharedPaths("`/box/shared/design/draft[old.md`")[0].path, "design/draft[old.md");
});

test("panel choices are remembered separately per room and DM, without a Files route", () => {
  const tabs = new Map<string, PanelTab>();
  assert.equal(panelTab(tabs, "a/", false), "sessions");
  assert.equal(panelTab(tabs, "a/worker", true), "about");
  tabs.set("a/", "files");
  tabs.set("a/worker", "sessions");
  assert.equal(panelTab(tabs, "a/", false), "files");
  assert.equal(panelTab(tabs, "a/worker", true), "sessions");
  assert.equal(panelTab(tabs, "b/", false), "sessions");
  tabs.set("a/worker", "files");
  assert.equal(panelTab(tabs, "a/worker", true), "files");
  tabs.set("a/worker", "about");
  assert.equal(panelTab(tabs, "a/worker", false), "sessions", "a closed DM cannot show About");
  assert.equal(roomRoute.test("/r/a/files/design/note.md"), false);
});

test("path media uses the message identity, a guessed type and the shared route", () => {
  const source = { key: "post-1", at: "2026-11-06T10:00:00Z", from: { kind: "session" as const, session: "builder" } };
  const item = sharedMedia("dyna", "design/a & b.png", source);
  assert.deepEqual(item, { ...source, room: "dyna", position: 0, src: sharedURL("dyna", "design/a & b.png"), sharedPath: "design/a & b.png", sizeKnown: false, file: { name: "a & b.png", media: "image/png", bytes: 0, sha256: "" } });
  assert.equal(mediaKind(sharedMedia("dyna", "a.go", source).file), "text");
  assert.equal(mediaKind(sharedMedia("dyna", "a.md", source).file), "markdown");
  assert.equal(mediaKind(sharedMedia("dyna", "a.svg", source).file), "svg");
  assert.equal(sharedMedia("dyna", "empty.txt", source, 0).sizeKnown, true);
  assert.equal(sharedMedia("dyna", "a.go", source, 123, "2026-11-05T09:00:00Z").modified, "2026-11-05T09:00:00Z");
});

test("a Range response supplies the full size, including an empty file", () => {
  assert.equal(sharedFileSize({ status: 206, headers: new Headers({ "Content-Range": "bytes 0-0/5824", "Content-Length": "1" }) }), 5824);
  assert.equal(sharedFileSize({ status: 200, headers: new Headers({ "Content-Length": "5824" }) }), 5824);
  assert.equal(sharedFileSize({ status: 416, headers: new Headers({ "Content-Range": "bytes */0" }) }), 0);
  assert.equal(sharedFileSize({ status: 404, headers: new Headers({ "Content-Length": "19" }) }), undefined);
  assert.equal(sharedFileSize({ status: 206, headers: new Headers({ "Content-Range": "bytes 0-0/*" }) }), undefined);
});

test("only expanded folders are flattened, with empty and retry rows", () => {
  const directory = (name: string) => ({ name, dir: true, size: 0, modified: "2026-11-06T10:00:00Z" });
  const listings = new Map([["", [directory("design"), { ...directory("note.md"), dir: false }]], ["design", [directory("nested")]], ["design/nested", []]]);
  assert.deepEqual(sharedRows(listings, new Set(), new Set()).map(row => row.path), ["design", "note.md"]);
  assert.deepEqual(sharedRows(listings, new Set(["design", "design/nested"]), new Set()).map(row => [row.kind, row.path, row.depth]), [["entry", "design", 0], ["entry", "design/nested", 1], ["empty", "design/nested", 2], ["entry", "note.md", 0]]);
  assert.equal(sharedRows(listings, new Set(["design"]), new Set(["design"]))[1].kind, "error");
});
