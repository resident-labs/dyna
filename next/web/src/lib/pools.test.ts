import { test } from "node:test";
import assert from "node:assert/strict";
import { enqueue, fieldChange, idleMinutes, poolLine, poolNodes, poolOrder, poolSummary, refusedField, requestFields } from "./pools.ts";
import type { HostSummary, Pool } from "./contract.gen.ts";

const pool = (name: string, more: Partial<Pool> = {}): Pool => ({ name, max: 4, nodes: 0, idle: 1_800_000, ...more });
const host = (name: string, pool?: string): HostSummary =>
  ({ name, pool, reachable: true, boxes: 0, cpus: 4, memBytes: 4, cpuUsed: 0, memUsed: 0, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null });

test("pools sort by name with digits as numbers, and the engine's list is not changed", () => {
  const items = [pool("gpu-10"), pool("ci-burst"), pool("gpu-2"), pool("eu-small"), pool("gpu-9")];
  assert.deepEqual(poolOrder(items).map(p => p.name), ["ci-burst", "eu-small", "gpu-2", "gpu-9", "gpu-10"]);
  assert.equal(items[0].name, "gpu-10");
});

test("a row shows the nodes of max and the idle minutes of a shared pool, or one per session", () => {
  assert.equal(poolLine(pool("a", { nodes: 3, max: 4, idle: 7_200_000 })), "3 of 4 nodes · idle 120 min");
  assert.equal(poolLine(pool("a", { nodes: 0, max: 1 })), "0 of 1 node · idle 30 min");
  assert.equal(poolLine(pool("a", { nodes: 2, max: 6, perSession: true, idle: undefined })), "2 of 6 nodes · one per session");
  assert.equal(poolLine(pool("a", { nodes: 1200, max: 2000, idle: 90_000 })), "1,200 of 2,000 nodes · idle 1.5 min");
});

test("idle minutes keep up to two decimals", () => {
  assert.equal(idleMinutes(1_800_000), 30);
  assert.equal(idleMinutes(90_000), 1.5);
  assert.equal(idleMinutes(100), 0);
  assert.equal(idleMinutes(1_000), 0.02);
  assert.equal(idleMinutes(0), 0);
});

test("a pool's nodes are the hosts of its pool by name with digits as numbers, and the rest are being created", () => {
  const hosts = [host("boats-10", "boats"), host("studio"), host("boats-9", "boats"), host("ships-1", "ships"), host("boats", "boatsx")];
  const nodes = poolNodes(pool("boats", { nodes: 4 }), hosts);
  assert.deepEqual(nodes.hosts.map(h => h.name), ["boats-9", "boats-10"]);
  assert.equal(nodes.creating, 2);
  // A host list newer than the pool list holds a node it does not count yet: none is being created.
  assert.equal(poolNodes(pool("boats", { nodes: 1 }), hosts).creating, 0);
  assert.equal(poolNodes(pool("boats", { nodes: 2 }), hosts).creating, 0);
  assert.equal(poolNodes(pool("boats", { nodes: 3 }), hosts).creating, 1);
});

test("the summary counts the pools, adds every pool's nodes and counts those failing", () => {
  assert.deepEqual(poolSummary([pool("a", { nodes: 3 }), pool("b", { nodes: 5, problem: "no capacity left" }), pool("c", { problem: "" })]), { pools: 3, nodes: 8, failing: 1 });
  assert.deepEqual(poolSummary([]), { pools: 0, nodes: 0, failing: 0 });
});

test("At most sends a whole number unlike the pool's, and leaves anything else to be put back", () => {
  const p = pool("boats", { max: 4 });
  assert.deepEqual(fieldChange(p, "max", "5"), { name: "boats", max: 5 });
  assert.deepEqual(fieldChange(p, "max", " 1 "), { name: "boats", max: 1 });
  // The engine refuses these, in its words.
  assert.deepEqual(fieldChange(p, "max", "0"), { name: "boats", max: 0 });
  assert.deepEqual(fieldChange(p, "max", "-3"), { name: "boats", max: -3 });
  assert.equal(fieldChange(p, "max", "4"), undefined);
  assert.equal(fieldChange(p, "max", "4.5"), undefined);
  assert.equal(fieldChange(p, "max", ""), undefined);
  assert.equal(fieldChange(p, "max", "  "), undefined);
  assert.equal(fieldChange(p, "max", "abc"), undefined);
  assert.equal(fieldChange(p, "max", "Infinity"), undefined);
});

test("Idle time sends minutes as milliseconds when they differ from what the field showed", () => {
  const p = pool("boats", { idle: 1_800_000 });
  assert.deepEqual(fieldChange(p, "idle", "45"), { name: "boats", idle: 2_700_000 });
  assert.deepEqual(fieldChange(p, "idle", "1.5"), { name: "boats", idle: 90_000 });
  assert.deepEqual(fieldChange(p, "idle", "0"), { name: "boats", idle: 0 });
  assert.deepEqual(fieldChange(p, "idle", "-1"), { name: "boats", idle: -60_000 });
  // Above zero is at least 1 ms, as 0 makes 30 minutes; below one minute rounds to the millisecond.
  assert.deepEqual(fieldChange(p, "idle", "0.000001"), { name: "boats", idle: 1 });
  assert.deepEqual(fieldChange(p, "idle", "0.00001"), { name: "boats", idle: 1 });
  assert.deepEqual(fieldChange(p, "idle", "0.0001"), { name: "boats", idle: 6 });
  assert.equal(fieldChange(p, "idle", "30"), undefined);
  assert.equal(fieldChange(p, "idle", ""), undefined);
  assert.equal(fieldChange(p, "idle", "x"), undefined);
  assert.equal(fieldChange(p, "idle", "Infinity"), undefined);
  // 100 ms shows as 0 minutes, so leaving the field as it was keeps it rather than making 30 minutes.
  assert.equal(fieldChange(pool("boats", { idle: 100 }), "idle", "0"), undefined);
  assert.deepEqual(fieldChange(pool("boats", { idle: 100 }), "idle", "1"), { name: "boats", idle: 60_000 });
});

test("a change made while one saves waits as its pool's one request, the later value winning for each field", () => {
  assert.deepEqual(enqueue([], { name: "boats", max: 5 }), [{ name: "boats", max: 5 }]);
  const both = enqueue([{ name: "boats", max: 5 }], { name: "boats", perSession: true });
  assert.deepEqual(both, [{ name: "boats", max: 5, perSession: true }]);
  assert.deepEqual(enqueue(both, { name: "boats", max: 7 }), [{ name: "boats", max: 7, perSession: true }]);
  assert.deepEqual(enqueue(both, { name: "boats", perSession: false }), [{ name: "boats", max: 5, perSession: false }]);
  // Another pool's waits behind, and a change to the second joins the second.
  const two = enqueue(both, { name: "ships", idle: 60_000 });
  assert.deepEqual(two, [{ name: "boats", max: 5, perSession: true }, { name: "ships", idle: 60_000 }]);
  assert.deepEqual(enqueue(two, { name: "ships", max: 2 }), [{ name: "boats", max: 5, perSession: true }, { name: "ships", idle: 60_000, max: 2 }]);
  assert.deepEqual(enqueue(two, { name: "boats", idle: 0 }), [{ name: "boats", max: 5, perSession: true, idle: 0 }, { name: "ships", idle: 60_000 }]);
  // A name holding another is another pool.
  assert.deepEqual(enqueue([{ name: "boats-2", max: 3 }], { name: "boats", max: 4 }), [{ name: "boats-2", max: 3 }, { name: "boats", max: 4 }]);
  // The queue it was given stays as it was.
  assert.deepEqual(both, [{ name: "boats", max: 5, perSession: true }]);
});

test("a refusal is about the first field the request sets, in the engine's order, max then idle then one per session", () => {
  assert.equal(refusedField({ name: "boats", max: 0, idle: -1, perSession: true }), "max");
  assert.equal(refusedField({ name: "boats", idle: -1, perSession: true }), "idle");
  assert.equal(refusedField({ name: "boats", idle: 0 }), "idle");
  assert.equal(refusedField({ name: "boats", max: 0 }), "max");
  assert.equal(refusedField({ name: "boats", perSession: false }), "perSession");
  assert.deepEqual(requestFields({ name: "boats", max: 0, idle: 0, perSession: false }), ["max", "idle", "perSession"]);
  assert.deepEqual(requestFields({ name: "boats", idle: 0 }), ["idle"]);
  assert.deepEqual(requestFields({ name: "boats" }), []);
});
