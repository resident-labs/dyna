import { test } from "node:test";
import assert from "node:assert/strict";
import { nextArea } from "./areas.ts";

// An area holds the nodes listed for it, as an element holds its descendants.
const area = (...nodes: string[]) => ({ contains: (node: Node | null) => nodes.includes(node as unknown as string) });
const at = (name: string) => name as unknown as Node;

test("F6 in Settings goes rail, Settings, list, detail and round again; Shift goes back", () => {
  // The section holds the nav, the list and the detail; the list and the detail are stops of their own inside it.
  const areas = [area("rail"), area("section", "nav", "listbox", "search", "detail", "switch"), area("listbox", "search"), area("detail", "switch")];
  const forward = (from: string) => nextArea(areas, at(from), false);
  assert.equal(forward("rail"), 1);
  assert.equal(forward("nav"), 2);
  assert.equal(forward("search"), 3);
  assert.equal(forward("switch"), 0);
  const back = (from: string) => nextArea(areas, at(from), true);
  assert.equal(back("rail"), 3);
  assert.equal(back("switch"), 2);
  assert.equal(back("listbox"), 1);
  assert.equal(back("nav"), 0);
});

test("with no area holding the focus, F6 goes to the first and Shift+F6 to the last", () => {
  const areas = [area("a"), area("b"), area("c")];
  assert.equal(nextArea(areas, null, false), 0);
  assert.equal(nextArea(areas, at("body"), true), 2);
});
