import assert from "node:assert/strict";
import test from "node:test";
import { parse } from "./markdown.ts";

test("thread text renders bold and list markdown", () => {
  const html = parse("**Done.** Two steps:\n\n- first\n- second");
  assert.match(html, /<strong>Done\.<\/strong>/);
  assert.match(html, /<li>first<\/li>/);
});

test("a post image becomes a link labeled with its alt text", () => {
  const html = parse("See ![Quarterly chart](https://example.com/q3.png)");
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /<a href="https:\/\/example\.com\/q3\.png">Quarterly chart<\/a>/);
});

test("a post image without alt text is labeled Image, and an unsafe image gets no link", () => {
  assert.match(parse("![](https://example.com/a.png)"), /<a href="https:\/\/example\.com\/a\.png">Image<\/a>/);
  const unsafe = parse("![Logo](javascript:alert(1))");
  assert.doesNotMatch(unsafe, /<a /);
  assert.doesNotMatch(unsafe, /javascript/);
  assert.match(unsafe, /Logo/);
});
