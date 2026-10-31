import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMentions } from "./mentions.ts";

test("a draft mentions open sessions by v1's rule, once each, and leaves other @text alone", () => {
  const names = new Set(["root", "lead-web", "parent"]);
  assert.deepEqual(parseMentions("@root, @lead-web please check (@root) and @lead-web.", names),
    { text: "@root, @lead-web please check (@root) and @lead-web.", mentions: ["root", "lead-web"] });
  // The engine reads @parent as a group, so a session of that name stays plain text.
  assert.deepEqual(parseMentions("ask @parent", names), { text: "ask @parent", mentions: [] });
  assert.deepEqual(parseMentions("@ghost mail me@root.dev, see `@root` or \\@root", names),
    { text: "@ghost mail me@root.dev, see `@root` or @root", mentions: [] });
  assert.deepEqual(parseMentions("@root", names), { text: "@root", mentions: ["root"] });
});
