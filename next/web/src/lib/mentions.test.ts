import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMentions } from "./mentions.ts";

test("a draft mentions open sessions by v1's rule, once each, and leaves other @text alone", () => {
  const names = new Set(["root", "lead-web"]);
  assert.deepEqual(parseMentions("@root, @lead-web please check (@root) and @lead-web.", names),
    { text: "please check (@root) and @lead-web.", mentions: ["root", "lead-web"] });
  assert.deepEqual(parseMentions("@ghost mail me@root.dev, see `@root` or \\@root", names),
    { text: "@ghost mail me@root.dev, see `@root` or \\@root", mentions: [] });
  assert.deepEqual(parseMentions("@root", names), { text: "@root", mentions: ["root"] });
});
