import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMentions } from "./mentions.ts";

test("a draft mentions open sessions by v1's rule, once each, and leaves other @text alone", () => {
  const names = [{ name: "root" }, { name: "lead-web", parent: "s1" }, { name: "parent", parent: "s1" }];
  assert.deepEqual(parseMentions("@root, @lead-web please check (@root) and @lead-web.", names),
    { text: "@root, @lead-web please check (@root) and @lead-web.", mentions: ["root", "lead-web"] });
  // The engine reads @parent as a group and @root as the room's root, so these stay plain text.
  assert.deepEqual(parseMentions("ask @parent", names), { text: "ask @parent", mentions: [] });
  assert.deepEqual(parseMentions("ask @root", [{ name: "root", parent: "s1" }]), { text: "ask @root", mentions: [] });
  assert.deepEqual(parseMentions("@ghost mail me@root.dev, see `@root` or \\@root", names),
    { text: "@ghost mail me@root.dev, see `@root` or @root", mentions: [] });
  assert.deepEqual(parseMentions("@root", names), { text: "@root", mentions: ["root"] });
});
