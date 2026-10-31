import { test } from "node:test";
import assert from "node:assert/strict";
import { hue } from "./identity.ts";

test("gives each name a stable hue that differs between similar names", () => {
  const names = ["lead-web", "builder-feed", "builder-feeds", "next-web-lead", "next-web-shell-b", "reviewer"];
  const hues = names.map(hue);
  assert.deepEqual(names.map(hue), hues);
  assert.equal(new Set(hues).size, names.length);
});
