import test from "node:test";
import assert from "node:assert/strict";
import { conversationMedia } from "./media.ts";
import type { MediaRow } from "./media.ts";

test("conversation media keeps order and excludes visual cards and other files", () => {
  const from = { kind: "owner" as const };
  const file = (name: string, media: string) => ({ name, media, bytes: 1, sha256: name });
  const rows: MediaRow[] = [
    { key: "p1", from, at: "now", attachments: [file("a", "image/png"), file("b", "text/plain"), { ...file("card", "image/png"), visual: { id: "v", version: 1, title: "Card", height: 100, allowNet: false, banner: "", thumbnail: "", shots: null, checks: null } }] },
    { key: "p2", from, at: "later", attachments: [file("c", "video/mp4")] },
  ];
  assert.deepEqual(conversationMedia(rows).map(item => [item.key, item.file.name]), [["p1", "a"], ["p2", "c"]]);
});
