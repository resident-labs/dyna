import { test } from "node:test";
import { strict as assert } from "node:assert";
import { roomIsLive, sourceLabel } from "./source.ts";
import type { RoomSummary } from "./contract.gen.ts";

test("a source links only to a live room and labels as room:session", () => {
  const room = (id: string, deleting?: boolean): [string, RoomSummary] => [id, { id, name: id, paused: false, created: "", lastRead: 0, waiting: null, working: null, failed: 0, hostless: 0, deleting }];
  const rooms = new Map([room("live"), room("going", true)]);
  assert.equal(roomIsLive(rooms, "live"), true);
  assert.equal(roomIsLive(rooms, "going"), false);
  assert.equal(roomIsLive(rooms, "missing"), false);
  assert.equal(sourceLabel({ room: "live", roomName: "Live", sessionName: "root" }), "Live:root");
});
