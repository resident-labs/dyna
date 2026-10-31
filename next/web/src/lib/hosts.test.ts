import { test } from "node:test";
import assert from "node:assert/strict";
import { hostMeters, lastSeen } from "./hosts.ts";

test("words a host's load, marks meters at 90% of their limit as busy, and has none without capacity", () => {
  const gib = 2 ** 30;
  const h = { name: "build-server", reachable: true, boxes: 1, cpus: 128, memBytes: 31 * gib, cpuUsed: 118.4, memUsed: 9.8 * gib,
    diskBytes: 931 * gib, diskUsed: 519 * gib, lastSeen: "2026-10-31T00:00:00Z", history: null };
  assert.deepEqual(hostMeters(h).map(({ text, percent, busy }) => ({ text, percent, busy })), [
    { text: "118 of 128 cores", percent: 93, busy: true }, { text: "9.8 of 31 GB", percent: 32, busy: false },
    { text: "412 GB free", percent: 56, busy: false }]);
  assert.deepEqual(hostMeters({ ...h, name: "control-plane", cpus: 0, memBytes: 0 }), []);
  assert.equal(lastSeen(h, Date.parse("2026-10-31T00:04:10Z")), "Last seen 4 min ago");
});
