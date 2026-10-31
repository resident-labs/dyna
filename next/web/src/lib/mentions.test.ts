import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMentions } from "./mentions.ts";

test("a draft mentions open sessions by v1's rule, once each, room-qualified, and leaves other @text alone", () => {
  const names = new Set(["root", "lead-web", "parent"]);
  assert.deepEqual(parseMentions("@root, @lead-web please check (@root) and @lead-web.", "studio", names),
    { text: "@root, @lead-web please check (@root) and @lead-web.", mentions: ["studio:root", "studio:lead-web"] });
  // A session named like a group address is still that one session.
  assert.deepEqual(parseMentions("ask @parent", "studio", names), { text: "ask @parent", mentions: ["studio:parent"] });
  assert.deepEqual(parseMentions("@ghost mail me@root.dev, see `@root` or \\@root", "studio", names),
    { text: "@ghost mail me@root.dev, see `@root` or @root", mentions: [] });
  assert.deepEqual(parseMentions("@root", "studio", names), { text: "@root", mentions: ["studio:root"] });
});
