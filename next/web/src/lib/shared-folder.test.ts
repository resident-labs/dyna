import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

test("Show in folder waits for the panel before revealing the target", async () => {
  // Exercise App's actual callback with a controlled panel-opening promise. A real browser may
  // mask the race when the first directory fetch takes longer than the sheet's layout.
  const app = readFileSync(new URL("../App.svelte", import.meta.url), "utf8");
  const callback = app.match(/  setSharedFolder\([\s\S]*?\n  \}\);/)?.[0];
  assert.ok(callback, "App registers Show in folder");
  let showFolder: (path: string) => void = () => assert.fail("Show in folder was not registered");
  let opened = false, finishOpening!: () => void;
  const opening = new Promise<void>(resolve => { finishOpening = resolve; });
  const tabs = new Map<string, string>();
  // The callback is JavaScript; these are the surrounding reactive bindings it owns in App.
  const state = new Function("setSharedFolder", "showPanel", "tabs", `
    let key = "room-a/", sharedItems = ["open viewer"], folderTarget;
    ${callback}
    return {
      current: () => ({ sharedItems, folderTarget }),
      navigate: value => { key = value; },
    };
  `)((handler: typeof showFolder) => { showFolder = handler; }, (open: boolean) => {
    assert.equal(open, true);
    opened = true;
    return opening;
  }, tabs) as {
    current(): { sharedItems: unknown[]; folderTarget?: { path: string; conversation: string; request: number } };
    navigate(key: string): void;
  };

  showFolder("design/notes.md");
  assert.equal(opened, true, "opens the sheet");
  assert.deepEqual(state.current().sharedItems, [], "closes the viewer");
  assert.equal(tabs.get("room-a/"), "files", "switches to Files before opening");
  assert.equal(state.current().folderTarget, undefined, "does not reveal in a hidden virtual list");

  // Navigation while the panel opens must not redirect the request to a different conversation.
  state.navigate("room-b/");
  finishOpening();
  await opening;
  assert.deepEqual(state.current().folderTarget, { path: "design/notes.md", conversation: "room-a/", request: 1 });

  state.navigate("room-a/");
  showFolder("design/notes.md");
  await opening;
  assert.deepEqual(state.current().folderTarget, { path: "design/notes.md", conversation: "room-a/", request: 2 }, "a repeated path still issues a new reveal");
});
