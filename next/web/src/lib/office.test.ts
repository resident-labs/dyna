import test from "node:test";
import assert from "node:assert/strict";
import { officeSizeLimit, officeTheme, parseOfficeFrameMessage, parseOfficeParentMessage, readOfficeBytes } from "./office.ts";
import { carbon } from "./themes.ts";

test("frame messages accept ready, rendered, error, close, and http(s) open links only", () => {
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:ready" }), { type: "office:ready" });
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:close" }), { type: "office:close" });
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:rendered", id: 3 }), { type: "office:rendered", id: 3 });
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:error", id: 4 }), { type: "office:error", id: 4 });
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:open", url: "https://example.com/a?b=1" }), { type: "office:open", url: "https://example.com/a?b=1" });
  assert.deepEqual(parseOfficeFrameMessage({ type: "office:open", url: "http://example.com" }), { type: "office:open", url: "http://example.com/" });
  assert.equal(parseOfficeFrameMessage({ type: "office:open", url: "javascript:alert(1)" }), null);
  assert.equal(parseOfficeFrameMessage({ type: "office:open", url: "data:text/html,hi" }), null);
  assert.equal(parseOfficeFrameMessage({ type: "office:open", url: "file:///etc/passwd" }), null);
  assert.equal(parseOfficeFrameMessage({ type: "office:rendered", id: "1" }), null);
  assert.equal(parseOfficeFrameMessage(null), null);
});

test("parent messages require valid kind, buffer, and theme tokens", () => {
  const theme = officeTheme(carbon);
  const buffer = new ArrayBuffer(4);
  assert.deepEqual(parseOfficeParentMessage({ type: "office:theme", theme }), { type: "office:theme", theme });
  const render = parseOfficeParentMessage({ type: "office:render", id: 1, kind: "word", name: "a.docx", buffer, theme });
  assert.equal(render?.type, "office:render");
  assert.equal(parseOfficeParentMessage({ type: "office:render", id: 1, kind: "pdf", name: "a.pdf", buffer, theme }), null);
  assert.equal(parseOfficeParentMessage({ type: "office:render", id: 1, kind: "word", name: "a.docx", buffer: new Uint8Array(4), theme }), null);
  assert.equal(parseOfficeParentMessage({ type: "office:theme", theme: { ...theme, mode: "neon" } }), null);
});

test("readOfficeBytes enforces the size limit via header and streaming bytes", async () => {
  const signal = new AbortController().signal;
  assert.equal(officeSizeLimit, 25 << 20);
  const ok = await readOfficeBytes("/f", signal, 8, async () => new Response(new Uint8Array([1, 2, 3, 4])));
  assert.ok(ok instanceof ArrayBuffer);
  assert.deepEqual([...new Uint8Array(ok)], [1, 2, 3, 4]);

  const byHeader = await readOfficeBytes("/f", signal, 8, async () => new Response(new Uint8Array(4), { headers: { "Content-Length": "9" } }));
  assert.equal(byHeader, "too-large");

  const byStream = await readOfficeBytes("/f", signal, 4, async () => new Response(new Uint8Array([1, 2, 3, 4, 5])));
  assert.equal(byStream, "too-large");

  await assert.rejects(readOfficeBytes("/f", signal, 8, async () => new Response("nope", { status: 404 })));
});
