import { test } from "node:test";
import assert from "node:assert/strict";
import { roomRoute } from "./routes.ts";

test("room instructions remain a room route", () => {
  assert.equal(roomRoute.exec("/r/studio/instructions")?.[1], "studio");
});
