import { test } from "node:test";
import assert from "node:assert/strict";
import { hostMeters } from "./hosts.ts";

test("words a host's load, marks meters at 90% of their limit as busy, and has none without capacity", () => {
  const gib = 2 ** 30;
  const h = { name: "build-server", reachable: true, boxes: 1, cpus: 16, memBytes: 31 * gib, cpuUsed: 14.4, memUsed: 9.8 * gib };
  assert.deepEqual(hostMeters(h).map(({ text, percent, busy }) => ({ text, percent, busy })), [
    { text: "14.4 of 16 cores", percent: 90, busy: true }, { text: "9.8 of 31 GB", percent: 32, busy: false }]);
  assert.deepEqual(hostMeters({ ...h, name: "control-plane", cpus: 0, memBytes: 0 }), []);
});
