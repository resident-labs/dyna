import assert from "node:assert/strict";
import test from "node:test";
import { startLabel } from "./start.ts";
import { hostState, machines } from "./hosts.ts";
import type { HostSummary } from "./contract.gen.ts";

test("startup step is typed, appears after a minute and resets its elapsed wait", () => {
  const start = { host: "arch", step: "binary" as const, startedAt: "2026-11-05T10:00:00Z" };
  const at = Date.parse(start.startedAt);
  assert.equal(startLabel(start, undefined, at + 59_999), "Starting on arch");
  assert.equal(startLabel(start, undefined, at + 60_000), "Starting on arch · updating Dyna on host (1 min)");
  assert.equal(startLabel(start, undefined, at + 180_000), "Starting on arch · updating Dyna on host (3 min)");
  assert.equal(startLabel({ ...start, step: "container_create", startedAt: new Date(at + 180_000).toISOString() }, undefined, at + 180_001), "Starting on arch");
  assert.equal(startLabel({ ...start, step: "container_create" }, undefined, at + 120_000), "Starting on arch · creating container (2 min)");
  assert.equal(startLabel(undefined, undefined, at), "Starting");
});

test("degradation stays separate from reachability on host views", () => {
  const host: HostSummary = { name: "arch", reachable: true, health: "degraded", shared: [], boxes: 1, cpus: 8, memBytes: 16, cpuUsed: 1, memUsed: 2, diskBytes: 100, diskUsed: 20, diskLow: false, history: [] };
  assert.deepEqual(hostState(host), { tone: "warning", label: "Degraded" });
  assert.deepEqual(hostState({ ...host, reachable: false }), { tone: "error", label: "Unreachable" });
  const merged = machines([{ ...host, name: "control-plane", health: "healthy", cpus: 8, memBytes: 16 }, { ...host, engineHost: true }]);
  assert.equal(merged[0].health, "degraded");
});
