import { test } from "node:test";
import assert from "node:assert/strict";
import { cardMessage, visualReplyFailure } from "./visual.ts";
import { decodeEvent } from "./event.ts";
import { APIError } from "./client.ts";

test("card messages accept bounded text and web links only", () => {
  assert.deepEqual(cardMessage({ type: "dyna:submit", text: "exact" }), { type: "submit", text: "exact" });
  assert.equal(cardMessage({ type: "dyna:submit", text: "x".repeat(16385) }), undefined);
  assert.equal(cardMessage({ type: "dyna:open", url: "javascript:alert(1)" }), undefined);
  assert.deepEqual(cardMessage({ type: "dyna:open", url: "https://example.org/" }), { type: "open", url: "https://example.org/" });
});

test("a superseded reply offers the newer version", () => {
  const visual = { event: "new-event", version: 12, seq: 42 };
  assert.deepEqual(visualReplyFailure(new APIError({ code: "visual_superseded", message: "stale", visual })), { kind: "superseded", visual });
  assert.deepEqual(visualReplyFailure(new Error("offline")), { kind: "failed" });
});

test("the event boundary rejects a malformed visual record", () => {
  const visual = { id: "v", version: 1, title: "Card", height: 600, allowNet: false, banner: "", thumbnail: "", shots: null, checks: null };
  const post = (card: unknown) => ({ seq: 1, id: "e", at: "", room: "r", from: { kind: "owner" }, type: "post.created",
    body: { text: "", home: { kind: "feed" }, attachments: [{ name: "card", media: "text/html", bytes: 1, sha256: "a", visual: card }] } });
  assert.equal(decodeEvent(post(visual))?.type, "post.created");
  assert.equal(decodeEvent(post({ ...visual, checks: [{ name: "widths", status: "invented" }] })), undefined);
});
