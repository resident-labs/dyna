import { test } from "node:test";
import assert from "node:assert/strict";
import { mentionToken, parseMentions } from "./mentions.ts";

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

test("the mention list opens on an @word that ends at the caret, by the mention rule", () => {
  assert.deepEqual(mentionToken("ask (@le", 8), { start: 5, query: "le" });
  assert.deepEqual(mentionToken("@", 1), { start: 0, query: "" });
  assert.equal(mentionToken("mail me@ro", 10), undefined);
  assert.equal(mentionToken("@lead now", 9), undefined);
});
