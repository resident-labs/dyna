import test from "node:test";
import assert from "node:assert/strict";
import { conversationMedia } from "./media.ts";
import type { Row } from "./feed.ts";

test("conversation media keeps order and excludes visual cards and other files", () => {
  const from = { kind: "owner" as const };
  const file = (name: string, media: string) => ({ name, media, bytes: 1, sha256: name });
  const rows: Row[] = [
    { kind: "post", key: "p1", from, at: "now", text: "", first: true, to: [], attachments: [file("a", "image/png"), file("b", "text/plain")] },
    { kind: "post", key: "p2", from, at: "later", text: "", first: true, to: [], attachments: [file("c", "video/mp4")] },
  ];
  assert.deepEqual(conversationMedia(rows).map(item => [item.key, item.file.name]), [["p1", "a"], ["p2", "c"]]);
});
