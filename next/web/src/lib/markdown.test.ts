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

test("a linked post image shows its label as one link", () => {
  assert.equal(parse("[![Logo](https://a.com/x.png)](https://b.com/)"), '<p><a href="https://b.com/">Logo</a></p>\n');
});

test("an image address and label keep each entity once", () => {
  assert.equal(
    parse("![A &amp; B](https://example.com/?a=1&amp;b=2)"),
    '<p><a href="https://example.com/?a=1&amp;b=2">A &amp; B</a></p>\n',
  );
});
