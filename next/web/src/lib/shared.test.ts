import test from "node:test";
import assert from "node:assert/strict";
import { filesURL, sharedMedia, sharedPaths, sharedURL } from "./shared.ts";
import { filesPath, roomRoute } from "./routes.ts";
import { mediaKind } from "./media.ts";

test("shared paths trim punctuation and closing quotes and brackets, ignoring private paths", () => {
  assert.deepEqual(sharedPaths('Read /box/shared/design/note.md.,:; and `/box/shared/a.go` or "/box/shared/b.png") /box/work/private /box/home/private').map(match => match.path), ["design/note.md", "a.go", "b.png"]);
  assert.equal(sharedPaths("/box/shared/").length, 0);
  assert.equal(sharedPaths("`/box/shared/design/draft[old.md`")[0].path, "design/draft[old.md");
});

test("Files routes preserve nested paths and encoded characters", () => {
  const url = filesURL("my room", "design/a # & note.md");
  assert.equal(roomRoute.exec(url)?.[1], "my%20room");
  assert.equal(roomRoute.exec(url)?.[2], undefined);
  assert.equal(filesPath(url), "design/a # & note.md");
  assert.equal(filesPath("/r/a/files"), "");
  assert.equal(filesPath("/r/a/files/"), "");
  assert.equal(filesPath("/r/a/files/%"), undefined);
  assert.equal(filesPath("/r/a/files/%2E%2E/secret"), undefined);
  assert.equal(filesPath("/r/a/files/a%2Fb"), undefined);
  assert.equal(filesPath("/r/a/files/a%5Cb"), undefined);
  assert.equal(filesPath("/r/a/s/b"), undefined);
});

test("path media uses the message identity, a guessed type and the shared route", () => {
  const source = { key: "post-1", at: "2026-11-06T10:00:00Z", from: { kind: "session" as const, session: "builder" } };
  const item = sharedMedia("dyna", "design/a & b.png", source);
  assert.deepEqual(item, { ...source, room: "dyna", position: 0, src: sharedURL("dyna", "design/a & b.png"), sharedPath: "design/a & b.png", file: { name: "a & b.png", media: "image/png", bytes: 0, sha256: "" } });
  assert.equal(mediaKind(sharedMedia("dyna", "a.go", source).file), "text");
  assert.equal(mediaKind(sharedMedia("dyna", "a.md", source).file), "markdown");
  assert.equal(mediaKind(sharedMedia("dyna", "a.svg", source).file), "svg");
});
