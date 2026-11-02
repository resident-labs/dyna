import assert from "node:assert/strict";
import test from "node:test";
import { parse, renderOnce, showMentions } from "./markdown.ts";

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

test("an image nested in formatting inside a link shows its label without a nested link", () => {
  assert.equal(
    parse("[**![Logo](https://a.com/x.png)**](https://b.com/)"),
    '<p><a href="https://b.com/"><strong>Logo</strong></a></p>\n',
  );
});

// The app's path without the sanitizer, which needs a browser: the render, its cache, then the room's names.
const sessions = new Map([["review-lead", "s1"], ["root", "s0"]]);
const shown = (text: string) => showMentions(renderOnce(parse)(text), "my room", sessions);
const pill = (name: string, id: string) => `<a href="/r/my%20room/s/${id}" data-mention="${name}">@${name}</a>`;

test("a known name links to its session wherever the mention rule finds it, without its punctuation", () => {
  assert.equal(shown("ask @review-lead, then (@root)."), `<p>ask ${pill("review-lead", "s1")}, then (${pill("root", "s0")}).</p>\n`);
  assert.equal(shown("@root"), `<p>${pill("root", "s0")}</p>\n`);
  assert.equal(shown("line\n@root"), `<p>line<br>${pill("root", "s0")}</p>\n`);
  for (const text of ["# @root", "- @root", "> @root", "**@root**", "| a |\n|---|\n| @root |"]) assert.ok(shown(text).includes(pill("root", "s0")), text);
});

test("an unknown handle and an email stay as they were", () => {
  assert.equal(shown("thanks @levzzz5154!"), "<p>thanks @levzzz5154!</p>\n");
  assert.equal(shown("@root.dev and @rootless"), "<p>@root.dev and @rootless</p>\n");
  assert.equal(shown("mail me@root.dev or a.b@root.dev"), '<p>mail <a href="mailto:me@root.dev">me@root.dev</a> or <a href="mailto:a.b@root.dev">a.b@root.dev</a></p>\n');
  assert.equal(shown("me@root, [@root and \\@root"), "<p>me@root, [@root and @root</p>\n");
});

test("no name links inside code, links or raw HTML, and a web address after an @ stays a link", () => {
  for (const text of ["`@root`", "``a @root``", "```\n@root\n```", "    @root", "[@root](https://a.com/)", "[see @root](https://a.com/)", "<https://a.com/@root>", "https://a.com/@root",
    '<a href="https://a.com/">@root</a>', "x <a href='https://a.com/'> @root</a>", "<code> @root</code>", "<div>@root</div>", "![@root](https://a.com/x.png)"]) {
    assert.doesNotMatch(shown(text), /data-mention/, text);
  }
  assert.match(shown("@https://a.com/"), /<a href="https:\/\/a\.com\/">https:\/\/a\.com\/<\/a>/);
  assert.match(shown("@www.a.com"), /<a href="http:\/\/www\.a\.com">www\.a\.com<\/a>/);
});

test("an author cannot write a candidate", () => {
  assert.equal(shown("﷐root﷑ and `﷐root﷑`"), "<p>root and <code>root</code></p>\n");
});

test("a text renders once however often and wherever it shows", () => {
  let renders = 0;
  const render = renderOnce(text => { renders++; return parse(text); });
  for (let i = 0; i < 1000; i++) {
    showMentions(render("ask @review-lead"), "a", sessions);
    showMentions(render("ask @review-lead"), "b", new Map());
    render("no name here");
  }
  assert.equal(renders, 2);
  // A session that appears later needs no new render, and a text with no name is one piece, the page's HTML unsplit.
  assert.equal(showMentions(render("ask @review-lead"), "b", new Map()), "<p>ask @review-lead</p>\n");
  assert.equal(showMentions(render("ask @review-lead"), "b", new Map([["review-lead", "s9"]])), `<p>ask <a href="/r/b/s/s9" data-mention="review-lead">@review-lead</a></p>\n`);
  assert.deepEqual(render("no name here"), ["<p>no name here</p>\n"]);
  assert.equal(renders, 2);
});
