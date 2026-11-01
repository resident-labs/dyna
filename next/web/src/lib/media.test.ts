import test from "node:test";
import assert from "node:assert/strict";
import { conversationMedia, decodePreview, hasSource, mediaKind, mediaSrc, mediaStep, parseTable, previewLimit, previewNote, readMedia, readPreview, sourceToggle, tableDelimiter } from "./media.ts";
import type { MediaRow } from "./media.ts";

const visual = { id: "v", version: 1, title: "Card", height: 100, allowNet: false, banner: "", thumbnail: "", shots: null, checks: null };

test("conversation media keeps every attachment in order", () => {
  const from = { kind: "owner" as const };
  const file = (name: string, media: string) => ({ name, media, bytes: 1, sha256: name });
  const rows: MediaRow[] = [
    { key: "p1", from, at: "now", attachments: [file("a", "image/png"), file("b", "text/plain"), { ...file("card", "image/png"), visual }] },
    { key: "p2", from, at: "later", attachments: [file("c", "video/mp4")] },
  ];
  assert.deepEqual(conversationMedia(rows).map(item => [item.key, item.position, item.file.name]), [["p1", 0, "a"], ["p1", 1, "b"], ["p1", 2, "card"], ["p2", 0, "c"]]);
});

test("duplicate attachments keep distinct positions in the same post", () => {
  const file = { name: "same.png", media: "image/png", bytes: 1, sha256: "same" };
  const items = conversationMedia([{ key: "p", from: { kind: "owner" }, at: "now", attachments: [file, file] }]);
  assert.deepEqual(items.map(item => [item.key, item.position]), [["p", 0], ["p", 1]]);
});

test("the media kind follows the declared type, then the extension of a type that says nothing", () => {
  const cases: [string, string, string][] = [
    ["a.png", "image/png", "image"], ["a.webp", "image/webp", "image"], ["a.bmp", "image/bmp", "image"],
    ["a.mp4", "video/mp4", "video"], ["a.webm", "video/webm", "video"],
    ["a.svg", "image/svg+xml", "svg"], ["a.html", "text/html", "html"], ["a.html", "text/html; charset=utf-8", "html"], ["A.HTM", "TEXT/HTML", "html"],
    ["a.md", "text/markdown", "markdown"], ["a.txt", "text/x-markdown", "markdown"], ["a.md", "application/octet-stream", "markdown"], ["a.MD", "", "markdown"], ["a.markdown", "text/plain", "markdown"],
    ["a.csv", "text/csv", "table"], ["a.tsv", "text/tab-separated-values", "table"], ["a.csv", "application/octet-stream", "table"], ["a.tsv", "text/plain", "table"],
    ["a.txt", "text/plain", "text"], ["a.json", "application/json", "text"], ["a.log", "application/octet-stream", "text"], ["a.log", "text/plain", "text"], ["a.ndjson", "application/x-ndjson", "text"],
    ["a.yaml", "application/yaml", "text"], ["a.go", "application/octet-stream", "text"], ["Dockerfile", "application/octet-stream", "text"], ["a.manifest", "application/ld+json", "text"],
    ["a.ts", "video/mp2t", "text"],
    ["a.pdf", "application/pdf", "pdf"], ["a.pdf", "application/octet-stream", "file"], ["a.zip", "application/zip", "file"], ["a.bin", "application/octet-stream", "file"], ["noextension", "", "file"],
    // The file route serves the declared type, so a page or drawing declared as anything else reads as its source.
    ["a.html", "text/plain", "text"], ["a.svg", "text/plain", "text"], ["a.html", "application/octet-stream", "text"], ["a.svg", "application/octet-stream", "text"],
  ];
  for (const [name, media, kind] of cases) assert.equal(mediaKind({ name, media }), kind, `${name} as ${media}`);
  assert.equal(mediaKind({ name: "card.html", media: "text/html", visual }), "visual");
});

test("a table takes its delimiter from the type or a plain type's extension", () => {
  assert.equal(tableDelimiter({ name: "a.dat", media: "text/csv" }), ",");
  assert.equal(tableDelimiter({ name: "a.dat", media: "text/tab-separated-values" }), "\t");
  assert.equal(tableDelimiter({ name: "a.csv", media: "application/json" }), undefined);
});

test("the rendered kinds offer their source, and the toggle says what comes next", () => {
  assert.deepEqual(["markdown", "table", "html", "svg", "visual"].filter(kind => hasSource(kind as never)).length, 5);
  assert.deepEqual(["image", "video", "text", "file"].filter(kind => hasSource(kind as never)), []);
  assert.equal(sourceToggle("markdown", true), "Show source");
  assert.equal(sourceToggle("markdown", false), "Show rendered markdown");
  assert.equal(sourceToggle("table", false), "Show table");
});

test("csv parsing keeps quotes, delimiters and line breaks inside a cell", () => {
  const { rows, width, moreRows, clipped } = parseTable('﻿name,note\n"Doe, Jane","said ""hi""\nand left"\r\nshort\n', ",");
  assert.deepEqual(rows, [["name", "note"], ["Doe, Jane", 'said "hi"\nand left'], ["short"]]);
  assert.deepEqual([width, moreRows, clipped], [2, false, false]);
  assert.deepEqual(parseTable("a\tb\n1\t2", "\t").rows, [["a", "b"], ["1", "2"]]);
  assert.deepEqual(parseTable("", ",").rows, []);
});

test("a table stops at its limits and says so", () => {
  const limits = { maxRows: 2, maxColumns: 2, maxCellLength: 3 };
  assert.deepEqual(parseTable("a\nb\nc\n", ",", false, limits), { rows: [["a"], ["b"]], width: 1, moreRows: true, clipped: false });
  assert.equal(parseTable("a\nb\n", ",", false, limits).moreRows, false);
  assert.deepEqual(parseTable("abcdef,2,3\n", ",", false, limits), { rows: [["abc", "2"]], width: 2, moreRows: false, clipped: true });
  // A cut file's last row is not whole, so it does not show.
  assert.deepEqual(parseTable("a,b\n1,2\n3,", ",", true).rows, [["a", "b"], ["1", "2"]]);
});

test("a preview stops at the limit, on a whole character", () => {
  const encoded = new TextEncoder().encode("ab€cd");
  assert.deepEqual(decodePreview(encoded, 3), { text: "ab", truncated: true });
  assert.deepEqual(decodePreview(encoded, 100), { text: "ab€cd", truncated: false });
  assert.deepEqual(decodePreview(encoded, encoded.length), { text: "ab€cd", truncated: false });
  assert.equal(decodePreview(new Uint8Array([104, 0, 105])), null);
});

test("a preview asks for one byte past the limit and stops even when the server ignores the range", async () => {
  let range = "";
  const answer = (body: string, status = 200): typeof fetch => async (_, init) => { range = new Headers(init?.headers).get("Range") ?? ""; return new Response(body, { status }); };
  const signal = new AbortController().signal;
  assert.deepEqual(await readPreview("/f", signal, 4, answer("abcdefgh")), { text: "abcd", truncated: true });
  assert.equal(range, "bytes=0-4");
  assert.deepEqual(await readPreview("/f", signal, 4, answer("abcd")), { text: "abcd", truncated: false });
  assert.deepEqual(await readPreview("/f", signal, 4, answer("")), { text: "", truncated: false });
  await assert.rejects(readPreview("/f", signal, 4, answer("nope", 404)));
});

test("a preview that is not the whole file says so", () => {
  const file = { bytes: 3 << 20 }, whole = { text: "", truncated: false }, cut = { text: "", truncated: true };
  const table = (count: number, moreRows: boolean, clipped = false) => ({ rows: Array.from({ length: count }, () => ["a"]), width: 1, moreRows, clipped });
  assert.equal(previewLimit, 1 << 20);
  assert.equal(previewNote(file, whole), "");
  assert.equal(previewNote(file, cut), "Showing the first 1.0 MB of 3.0 MB. Download the file for the rest.");
  assert.equal(previewNote(file, whole, table(1000, true)), "Showing the first 1,000 rows. Download the file for the rest.");
  assert.equal(previewNote(file, cut, table(812, false)), "Showing the first 812 rows, from the first 1.0 MB of 3.0 MB. Download the file for the rest.");
  assert.equal(previewNote(file, whole, table(3, false, true)), "Some long cells and wide rows are cut. Download the file for the rest.");
  assert.equal(previewNote(file, whole, table(3, false)), "");
});

test("arrow keys move through the media unless a scroller needs them", () => {
  assert.equal(mediaStep("ArrowLeft", 3, false), -1);
  assert.equal(mediaStep("ArrowRight", 3, false), 1);
  assert.equal(mediaStep("ArrowRight", 3, true), 0);
  assert.equal(mediaStep("ArrowRight", 1, false), 0);
  assert.equal(mediaStep("Enter", 3, false), 0);
});

test("a read's image is a media row served from the transcript", () => {
  const row = readMedia("s/1", "t 1", "2026-01-01T00:00:00Z", "/work/shots/mod-api-overview-dark.png", { item: "call|1", media: "image/png", bytes: 9 });
  const [item] = conversationMedia([row]);
  assert.equal(mediaSrc(item), "/v1/files/transcript?session=s%2F1&turn=t+1&item=call%7C1");
  assert.deepEqual([item.key, item.file.name, item.file.media, item.file.bytes, item.from.session], ["call|1", "mod-api-overview-dark.png", "image/png", 9, "s/1"]);
  assert.equal(mediaKind(item.file), "image");
  assert.equal(mediaSrc({ file: { name: "a", media: "", bytes: 1, sha256: "a/b" } }), "/v1/files/a%2Fb");
});
