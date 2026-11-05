import { test } from "node:test";
import assert from "node:assert/strict";
import { paneCounts, pathItem, roomRoute, settingsPanes } from "./routes.ts";

test("the Settings nav counts the rooms Approvals lists once they load, and nothing else", () => {
  assert.deepEqual(paneCounts(undefined), {});
  assert.deepEqual(paneCounts([]), { approvals: 0 });
  assert.deepEqual(paneCounts([{}, { deleting: false }, { deleting: true }]), { approvals: 2 });
});

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

test("Pools is an admin list page between Hosts and Images, under the line above Hosts", () => {
  assert.deepEqual(settingsPanes.slice(-3).map(pane => [pane.id, pane.label, pane.icon, pane.admin, !!pane.list, !!pane.sep]),
    [["hosts", "Hosts", "monitor", true, true, true], ["pools", "Pools", "stack", true, true, false], ["images", "Images", "box", true, true, false]]);
});

test("Workflows is an admin list page between Approvals and Skills & tools", () => {
  const at = settingsPanes.findIndex(pane => pane.id === "workflows");
  assert.deepEqual(settingsPanes.slice(at - 1, at + 2).map(pane => pane.id), ["approvals", "workflows", "addons"]);
  const pane = settingsPanes[at];
  assert.deepEqual([pane.label, pane.icon, pane.admin, !!pane.list, !!pane.sep], ["Workflows", "flow", true, true, false]);
});
