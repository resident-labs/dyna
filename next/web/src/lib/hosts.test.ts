import { test } from "node:test";
import assert from "node:assert/strict";
import { hostLoads, hostMeters, lastSeen } from "./hosts.ts";

test("words a host's load, marks meters at 90% of their limit as busy, and has none without capacity", () => {
  const gib = 2 ** 30;
  const h = { name: "build-server", reachable: true, boxes: 1, cpus: 128, memBytes: 31 * gib, cpuUsed: 118.4, memUsed: 9.8 * gib,
    diskBytes: 2048 * gib, diskUsed: 700 * gib, diskLow: false, lastSeen: "2026-10-31T00:00:00Z", history: null };
  assert.deepEqual(hostMeters(h).map(({ text, percent, busy }) => ({ text, percent, busy })), [
    { text: "118 of 128 cores", percent: 93, busy: true }, { text: "9.8 of 31 GB", percent: 32, busy: false },
    { text: `${(1348).toLocaleString()} GB free`, percent: 34, busy: false }]);
  assert.deepEqual(hostMeters({ ...h, name: "control-plane", cpus: 0, memBytes: 0 }), []);
  assert.equal(lastSeen(h, Date.parse("2026-10-31T00:04:10Z")), "Last seen 4 min ago");
});

test("the panel foot shows unreachable hosts first, then those low on disk, then reachable ones busiest first by CPU or memory, at most 3", () => {
  const host = (name: string, cpuUsed: number, memUsed: number, reachable = true) =>
    ({ name, reachable, boxes: 0, cpus: 10, memBytes: 100, cpuUsed, memUsed, diskBytes: 0, diskUsed: 0, diskLow: false, history: null });
  const hosts = [host("calm", 1, 10), host("cpu", 9.5, 10), host("down", 10, 100, false), { ...host("control-plane", 0, 0), cpus: 0 },
    host("mem", 2, 60), host("quiet", 0.5, 5)];
  const lines = (list: typeof hosts) => hostLoads(list).map(h => [h.name, ...h.reachable ? h.meters.map(m => `${m.label} ${m.percent}%${m.busy ? " busy" : ""}`) : ["Unreachable"]]);
  assert.deepEqual(lines(hosts), [["down", "Unreachable"], ["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"]]);
  assert.deepEqual(hostLoads([...hosts, { ...host("full", 0.1, 1), diskLow: true }]).map(h => h.name), ["down", "full", "cpu"]);
  assert.deepEqual(lines(hosts.filter(h => h.reachable)), [["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"], ["calm", "CPU 10%", "Memory 10%"]]);
});
