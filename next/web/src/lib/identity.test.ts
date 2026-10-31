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
  const labels = distinctInitials(["root", "docs-site", "design-system", "dove", "d1", "reviewer-2", "reviewer-22", "reviewer-27", "reviewer-3", "reviewer-347", "rex-72"]);
  assert.deepEqual(Object.fromEntries(labels), {
    d1: "D1", "design-system": "DS", "docs-site": "DO", dove: "D2", root: "RO",
    // Digits of the name's own come before a counting digit: the last one, then the others.
    "reviewer-2": "R2", "reviewer-22": "RE", "reviewer-27": "R7", "reviewer-3": "R3", "reviewer-347": "R4", "rex-72": "R1",
  });
});
