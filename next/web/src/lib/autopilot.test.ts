import { test } from "node:test";
import assert from "node:assert/strict";
import { approvalChips, approvalOn, approvalsLine, autopilotConfirm, autopilotOn, autopilotRequest, rootApprovals, switchRequest } from "./autopilot.ts";
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

test("the page lists only the four kinds the engine has", () => {
  assert.deepEqual(rootApprovals.map(item => item.kind), ["hostexec", "addons", "hosts", "pools"]);
  assert.equal(rootApprovals.find(item => item.kind === "hosts")?.label, "Root changes room host rules");
});

test("Autopilot asks, naming all four and the cost of pools, before turning on, and asks nothing to turn off", () => {
  const ask = autopilotConfirm(room(["hosts"]));
  assert.ok(ask);
  for (const item of rootApprovals) assert.ok(ask.message.includes(item.label.replace(/^Root /, "")), item.label);
  assert.match(ask.note, /cost money/);
  assert.equal(autopilotConfirm(room(["hostexec", "addons", "hosts", "pools"])), undefined);
});

test("the chips split rooms into Autopilot, some on and all off, and a narrow row names what is on", () => {
  const rooms = [room(["hostexec", "addons", "hosts", "pools"]), room(["addons", "hosts"]), room([]), room()];
  const counts = approvalChips.map(chip => rooms.filter(chip.test ?? (() => true)).length);
  assert.deepEqual(approvalChips.map(chip => chip.label), ["All", "Autopilot", "Some on", "All off"]);
  assert.deepEqual(counts, [4, 1, 1, 2]);
  assert.deepEqual(rooms.map(approvalsLine), ["Autopilot", "Add-ons, Host rules", "All off", "All off"]);
});
