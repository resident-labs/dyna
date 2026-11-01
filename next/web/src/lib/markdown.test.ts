import assert from "node:assert/strict";
import test from "node:test";
import { parse } from "./markdown.ts";

test("thread text renders bold and list markdown", () => {
  const html = parse("**Done.** Two steps:\n\n- first\n- second");
  assert.match(html, /<strong>Done\.<\/strong>/);
  assert.match(html, /<li>first<\/li>/);
});
