import { test } from "node:test";
import assert from "node:assert/strict";
import { hue, distinctInitials } from "./identity.ts";

test("gives each name a stable hue that differs between similar names", () => {
  const names = ["lead-web", "builder-feed", "builder-feeds", "next-web-lead", "next-web-shell-b", "reviewer"];
  const hues = names.map(hue);
  assert.deepEqual(names.map(hue), hues);
  assert.equal(new Set(hues).size, names.length);
});

test("gives names initials that tell them apart", () => {
  const many = Array.from({ length: 60 }, (_, i) => `bench-fan-${i}`);
  assert.equal(new Set(distinctInitials(many).values()).size, many.length);
  const rooms = distinctInitials(["root", "docs-site", "design-system", "dove"]);
  assert.deepEqual(Object.fromEntries(rooms), { "design-system": "DS", dove: "DO", root: "RO", "docs-site": "D1" });
  // A name keeps its own initials before others fall back; digits of the name's own come before a counting digit.
  const sessions = distinctInitials(["reviewer-2", "reviewer-7", "reviewer-22", "reviewer-27", "reviewer-3", "reviewer-347", "tester-2", "tester-28", "tester-29"]);
  assert.deepEqual(Object.fromEntries(sessions), {
    "reviewer-2": "R2", "reviewer-3": "R3", "reviewer-7": "R7", "tester-2": "T2",
    "reviewer-22": "RE", "reviewer-27": "R1", "reviewer-347": "R4", "tester-28": "TE", "tester-29": "T9",
  });
});
