import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { statusOf, countOf, urgency, sessionCountsLine } from "./status.ts";

test("background waits do not reuse urgent owner waiting or mask work and failure", () => {
  const facts = { broken: false, waiting: false, working: false, failed: false, background: true };
  assert.equal(statusOf(facts), "background");
  assert.equal(statusOf({ ...facts, working: true }), "working");
  assert.equal(statusOf({ ...facts, failed: true }), "failed");
  assert.equal(statusOf({ ...facts, waiting: true, working: true }), "waiting");
  assert.equal(statusOf({ ...facts, broken: true, waiting: true }), "failed");
  assert.ok(urgency("waiting") < urgency("background"));
  assert.equal(countOf(["background", "waiting", "working"]).background, 1);
});

test("session summary separates background waits from needs-you and counts priority once", () => {
  const plain = { broken: false, waiting: false, working: false, failed: false };
  const statuses = [
    statusOf({ ...plain, working: true }), statusOf({ ...plain, working: true }),
    statusOf({ ...plain, background: true }), statusOf({ ...plain, background: true }), statusOf({ ...plain, background: true }),
    statusOf({ ...plain, waiting: true, working: true, background: true }),
    statusOf({ ...plain, broken: true, waiting: true, background: true }), statusOf(plain),
  ];
  const counts = countOf(statuses);
  assert.equal(sessionCountsLine(counts), "2 working · 3 waiting · 1 needs you");
  assert.equal(sessionCountsLine({ ...counts, waiting: 2 }), "2 working · 3 waiting · 2 need you");
  assert.deepEqual(counts, { sessions: 8, working: 2, background: 3, waiting: 1, failed: 1, idle: 1 });
  assert.equal(counts.working + counts.background + counts.waiting + counts.failed + counts.idle, counts.sessions);
});

test("a truncated session reason has the complete line as its title", () => {
  const row = readFileSync(new URL("./ui/SessionRow.svelte", import.meta.url), "utf8");
  assert.match(row, /<span class="line"[^>]*\btitle=\{line\}/);
});
