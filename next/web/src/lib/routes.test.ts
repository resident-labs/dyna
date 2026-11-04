import { test } from "node:test";
import assert from "node:assert/strict";
import { pathItem, roomRoute, settingsPanes } from "./routes.ts";

test("room instructions and new-session pages remain room routes", () => {
  assert.equal(roomRoute.exec("/r/studio/instructions")?.[1], "studio");
  assert.equal(roomRoute.exec("/r/studio/new")?.[1], "studio");
});

test("the root-approves page is the admin Approvals pane", () => {
  const pane = settingsPanes.find(item => item.id === "approvals");
  assert.equal(pane?.label, "Approvals");
  assert.equal(pane?.admin, true);
});

test("a list page's item is the one decoded part after its prefix, and a malformed one is no item", () => {
  const prefix = "/settings/approvals/";
  assert.equal(pathItem("/settings/approvals/room_a", prefix), "room_a");
  for (const id of ["a/b", "two words", "%", "dyna ✓ ドキュメント", "100%25"]) assert.equal(pathItem(prefix + encodeURIComponent(id), prefix), id);
  assert.equal(pathItem(prefix + "a%2Fb", prefix), "a/b");
  for (const path of ["/settings/approvals/%", "/settings/approvals/%E0%A4%A", "/settings/approvals/%ZZ", "/settings/approvals/", "/settings/approvals", "/settings/approvals/a/b", "/settings/profiles/a"])
    assert.equal(pathItem(path, prefix), undefined, path);
});
