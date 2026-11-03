import { test } from "node:test";
import assert from "node:assert/strict";
import { approvalOn, autopilotOn, autopilotRequest, rootApprovals, switchRequest } from "./autopilot.ts";
import type { RoomSummary } from "./contract.gen.ts";

const room = (rootApproves?: RoomSummary["rootApproves"]) => ({ id: "room_a", name: "web", rootApproves }) as RoomSummary;

test("every switch is off for a room the engine lists no kinds for", () => {
  for (const { kind } of rootApprovals) assert.equal(approvalOn(room(), kind), false);
  assert.equal(autopilotOn(room()), false);
  assert.equal(autopilotOn(room([])), false);
});

test("a switch shows on for the kinds listed, and its request changes only its own kind", () => {
  const web = room(["pools"]);
  assert.deepEqual(rootApprovals.map(({ kind }) => approvalOn(web, kind)), [false, false, false, true]);
  assert.deepEqual(switchRequest(web, "hostexec", true), { room: "room_a", rootApproves: { hostexec: true, addons: false, hosts: false, pools: true } });
  assert.deepEqual(switchRequest(web, "pools", false), { room: "room_a", rootApproves: { hostexec: false, addons: false, hosts: false, pools: false } });
});

test("Autopilot turns every kind on, and off again once they all are", () => {
  assert.deepEqual(autopilotRequest(room(["hosts"])), { room: "room_a", rootApproves: { hostexec: true, addons: true, hosts: true, pools: true } });
  const all = room(["hostexec", "addons", "hosts", "pools"]);
  assert.equal(autopilotOn(all), true);
  assert.deepEqual(autopilotRequest(all), { room: "room_a", rootApproves: { hostexec: false, addons: false, hosts: false, pools: false } });
});
