import { test } from "node:test";
import assert from "node:assert/strict";
import { roomRoute } from "./routes.ts";

test("room instructions and new-session pages remain room routes", () => {
  assert.equal(roomRoute.exec("/r/studio/instructions")?.[1], "studio");
  assert.equal(roomRoute.exec("/r/studio/new")?.[1], "studio");
});
