import { test } from "node:test";
import assert from "node:assert/strict";
import { hue, distinctInitials } from "./identity.ts";

test("gives each name a stable hue that differs between similar names", () => {
  const names = ["lead-web", "builder-feed", "builder-feeds", "next-web-lead", "next-web-shell-b", "reviewer"];
  const hues = names.map(hue);
  assert.deepEqual(names.map(hue), hues);
  assert.equal(new Set(hues).size, names.length);
});

test("gives rooms initials that tell them apart", () => {
  const labels = distinctInitials(["root", "docs-site", "design-system", "dove", "d1"]);
  assert.deepEqual(Object.fromEntries(labels), { d1: "D1", "design-system": "DS", "docs-site": "DO", dove: "D2", root: "RO" });
});
