import { test } from "node:test";
import assert from "node:assert/strict";
import { backupProblem, hostLabel, hostLoads, hostMeters, largeShared, lastSeen, machines } from "./hosts.ts";

test("labels the Dyna host without implying that it runs this browser", () => {
  assert.equal(hostLabel("control-plane"), "Dyna host");
  assert.equal(hostLabel("build-server"), "build-server");
});

test("one machine reported as the control plane and a registered host is one card, named for the host, with whole-machine figures", () => {
  const gib = 2 ** 30;
  const plane = { name: "control-plane", reachable: true, boxes: 0, cpus: 20, memBytes: 31 * gib, cpuUsed: 0.2, memUsed: 4 * gib, diskBytes: 1900 * gib, diskUsed: 1192 * gib,
    diskLow: false, shared: [], history: null, backupCopyEnabled: true, lastSeen: "2026-10-31T00:00:00Z" };
  const lab = { name: "lab", reachable: true, boxes: 3, cpus: 19, memBytes: 27 * gib, cpuUsed: 0, memUsed: 0, diskBytes: 1900 * gib, diskUsed: 1192 * gib,
    diskLow: false, shared: [{ room: "r", bytes: gib }], history: null, lastSeen: "2026-10-31T00:00:05Z", engineHost: true };
  const other = { ...lab, name: "gpu-box", engineHost: undefined, boxes: 1 };
  const [one, ...rest] = machines([plane, lab, other]);
  assert.deepEqual(rest, [other]);
  assert.deepEqual([one.name, one.boxes, one.shared], ["lab", 3, lab.shared]);
  assert.deepEqual(hostMeters(one).map(m => m.text), hostMeters(plane).map(m => m.text));
  assert.equal(one.backupCopyEnabled, true);
  assert.equal(hostLabel(one.name), "lab");
  // Either side's low disk shows, and a host the engine cannot reach still says so.
  assert.equal(machines([{ ...plane, diskLow: true }, lab])[0].diskLow, true);
  assert.equal(machines([plane, { ...lab, diskLow: true }])[0].diskLow, true);
  assert.equal(machines([plane, { ...lab, reachable: false }])[0].reachable, false);
  // With no registered host on the engine's machine, or none yet compared, both stay.
  assert.deepEqual(machines([plane, { ...lab, engineHost: false }]).map(h => h.name), ["control-plane", "lab"]);
  assert.deepEqual(machines([plane, other]).map(h => h.name), ["control-plane", "gpu-box"]);
  assert.deepEqual(machines([lab]), [lab]);
  // Before the control plane's first sample there are no whole-machine figures to show.
  assert.deepEqual(machines([{ ...plane, memBytes: 0 }, lab]).map(h => h.name), ["control-plane", "lab"]);
});

test("words a host's load, marks meters at 90% of their limit as busy, and has none without capacity", () => {
  const gib = 2 ** 30;
  const h = { name: "build-server", reachable: true, boxes: 1, cpus: 128, memBytes: 31 * gib, cpuUsed: 118.4, memUsed: 9.8 * gib,
    diskBytes: 2048 * gib, diskUsed: 700 * gib, diskLow: false, shared: [], lastSeen: "2026-10-31T00:00:00Z", history: null };
  assert.deepEqual(hostMeters(h).map(({ text, percent, busy }) => ({ text, percent, busy })), [
    { text: "118 of 128 cores", percent: 93, busy: true }, { text: "9.8 of 31 GB", percent: 32, busy: false },
    { text: `${(1348).toLocaleString()} GB free`, percent: 34, busy: false }]);
  assert.deepEqual(hostMeters({ ...h, name: "control-plane", cpus: 0, memBytes: 0 }), []);
  assert.equal(lastSeen(h, Date.parse("2026-10-31T00:04:10Z")), "Last seen 4 min ago");
});

test("the panel foot shows unreachable hosts first, then those low on disk, then reachable ones busiest first by CPU or memory, at most 3", () => {
  const host = (name: string, cpuUsed: number, memUsed: number, reachable = true) =>
    ({ name, reachable, boxes: 0, cpus: 10, memBytes: 100, cpuUsed, memUsed, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null });
  const hosts = [host("calm", 1, 10), host("cpu", 9.5, 10), host("down", 10, 100, false), { ...host("control-plane", 0, 0), cpus: 0 },
    host("mem", 2, 60), host("quiet", 0.5, 5)];
  const lines = (list: typeof hosts) => hostLoads(list).map(h => [h.name, ...h.reachable ? h.meters.map(m => `${m.label} ${m.percent}%${m.busy ? " busy" : ""}`) : ["Unreachable"]]);
  assert.deepEqual(lines(hosts), [["down", "Unreachable"], ["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"]]);
  assert.deepEqual(hostLoads([...hosts, { ...host("full", 0.1, 1), diskLow: true }]).map(h => h.name), ["down", "full", "cpu"]);
  assert.deepEqual(lines(hosts.filter(h => h.reachable)), [["cpu", "CPU 95% busy", "Memory 10%"], ["mem", "CPU 20%", "Memory 60%"], ["calm", "CPU 10%", "Memory 10%"]]);
});

test("Home marks a stored backup failure or a copy over 48 hours old, and nothing else", () => {
  const now = Date.parse("2026-10-31T12:00:00Z");
  const h = { name: "control-plane", reachable: true, boxes: 0, cpus: 8, memBytes: 8, cpuUsed: 1, memUsed: 1, diskBytes: 0, diskUsed: 0, diskLow: false, shared: [], history: null };
  const on = { ...h, backupCopyEnabled: true };
  assert.equal(backupProblem(h, now), undefined);
  assert.equal(backupProblem(on, now), undefined);
  assert.equal(backupProblem({ ...on, backupCopyAt: "2026-10-29T12:00:00Z" }, now), undefined);
  assert.deepEqual(backupProblem({ ...on, backupCopyAt: "2026-10-28T11:00:00Z" }, now), { tone: "warning", label: "Backup 3 days old" });
  assert.deepEqual(backupProblem({ ...on, backupCopyAt: "2026-10-31T00:00:00Z", backupCopyFailure: "no_space" }, now), { tone: "error", label: "Backup machine full", title: "The second machine has no room for the backup" });
  assert.equal(backupProblem({ ...on, backupCopyFailure: "failed" }, now)?.label, "Backup failed");
});

test("Home names a room's shared folder only past a quarter of its host's disk, largest first", () => {
  const host = (diskBytes: number, shared: { room: string; bytes: number }[] | null) => ({ name: "build-server", reachable: true, boxes: 1, cpus: 8, memBytes: 0,
    cpuUsed: 0, memUsed: 0, diskBytes, diskUsed: 0, diskLow: false, shared, history: null });
  const quarter = 100 * 2 ** 30;
  assert.deepEqual(largeShared(host(4 * quarter, [{ room: "a", bytes: quarter }, { room: "b", bytes: quarter + 1 }, { room: "c", bytes: 2 * quarter }])).map(f => f.room), ["c", "b"]);
  assert.deepEqual(largeShared(host(4 * quarter, null)), []);
  assert.deepEqual(largeShared(host(0, [{ room: "a", bytes: quarter }])), []);
});
